package content

// note_separation_integration_test.go — DB-gated acceptance coverage of the
// "对话不进笔记" model: conversation messages live only in message_blocks
// (immutable transcript), the note tree holds only manual blocks and saved
// message excerpts, and frozen renders never carry dialogue formatting.
// Runs only against a migrated database.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"agentchunzhi/internal/noteblocks"
)

func sepKey(prefix string) string {
	return fmt.Sprintf("sep-%s-%d", prefix, time.Now().UnixNano())
}

// TestMessagesNeverEnterNoteTree proves the core separation rule: chatting
// leaves the note tree (blocks, tree revision, draft epoch) untouched while
// the transcript stays complete in message_blocks.
func TestMessagesNeverEnterNoteTree(t *testing.T) {
	database := patternTestStore(t)
	service := Service{Store: database}
	ctx := context.Background()
	principal, conversationID, _ := patternFixture(t, database)

	before, beforeRevision, err := service.NoteBlocks(ctx, principal, conversationID)
	if err != nil || len(before) != 2 {
		t.Fatalf("baseline note blocks: %v count=%d", err, len(before))
	}
	var beforeDraftRevision int64
	if err := database.Pool.QueryRow(ctx, `
		SELECT d.revision FROM asset.asset_drafts d
		JOIN content.note_bindings nb ON nb.note_asset_id = d.asset_id AND nb.organization_id = d.organization_id
		WHERE nb.conversation_id = $1::uuid
	`, conversationID).Scan(&beforeDraftRevision); err != nil {
		t.Fatalf("baseline draft revision: %v", err)
	}

	if _, err := service.AppendMessage(ctx, principal, sepKey("human"), AppendMessageInput{
		ConversationID: conversationID, Role: "user", Content: "用户的问题原文", ContentFormat: "plain_text", Status: "completed",
	}); err != nil {
		t.Fatalf("append user message: %v", err)
	}
	if _, err := service.AppendMessage(ctx, principal, sepKey("ai"), AppendMessageInput{
		ConversationID: conversationID, Role: "assistant", Content: "助手的回答原文", ContentFormat: "markdown", Status: "completed",
	}); err != nil {
		t.Fatalf("append assistant message: %v", err)
	}

	after, afterRevision, err := service.NoteBlocks(ctx, principal, conversationID)
	if err != nil || len(after) != 2 {
		t.Fatalf("messages must not grow the note tree: %v count=%d", err, len(after))
	}
	if afterRevision != beforeRevision {
		t.Fatalf("messages must not advance the tree revision: %d -> %d", beforeRevision, afterRevision)
	}
	var afterDraftRevision int64
	if err := database.Pool.QueryRow(ctx, `
		SELECT d.revision FROM asset.asset_drafts d
		JOIN content.note_bindings nb ON nb.note_asset_id = d.asset_id AND nb.organization_id = d.organization_id
		WHERE nb.conversation_id = $1::uuid
	`, conversationID).Scan(&afterDraftRevision); err != nil {
		t.Fatalf("after draft revision: %v", err)
	}
	if afterDraftRevision != beforeDraftRevision {
		t.Fatalf("messages must not dirty the note draft: %d -> %d", beforeDraftRevision, afterDraftRevision)
	}

	messages, err := service.ListMessages(ctx, principal, conversationID)
	if err != nil || len(messages) != 2 {
		t.Fatalf("transcript must stay complete: %v count=%d", err, len(messages))
	}
	if messages[0].Role != "user" || messages[1].Role != "assistant" {
		t.Fatalf("unexpected transcript order: %+v", messages)
	}

	view, err := service.NoteView(ctx, principal, conversationID)
	if err != nil {
		t.Fatalf("note view: %v", err)
	}
	if strings.Contains(view.DraftMarkdown, "用户的问题原文") || strings.Contains(view.DraftMarkdown, "助手的回答原文") {
		t.Fatalf("draft render must not carry conversation messages: %q", view.DraftMarkdown)
	}
	if strings.Contains(view.DraftMarkdown, "## User") || strings.Contains(view.DraftMarkdown, "## Assistant") {
		t.Fatalf("draft render must not carry dialogue headings: %q", view.DraftMarkdown)
	}
}

// TestSaveMessageExcerptToNote covers the single deliberate entry for
// conversation content into the note: saving one message as an excerpt
// block, with provenance, independent editability, and validation.
func TestSaveMessageExcerptToNote(t *testing.T) {
	database := patternTestStore(t)
	service := Service{Store: database}
	ctx := context.Background()
	principal, conversationID, _ := patternFixture(t, database)

	answer, err := service.AppendMessage(ctx, principal, sepKey("ans"), AppendMessageInput{
		ConversationID: conversationID, Role: "assistant", Content: "值得存进笔记的结论", ContentFormat: "markdown", Status: "completed",
	})
	if err != nil {
		t.Fatalf("append answer: %v", err)
	}

	saved, err := service.AddNoteBlock(ctx, principal, sepKey("save"), conversationID, "", "", answer.BlockRevisionID)
	if err != nil {
		t.Fatalf("save excerpt: %v", err)
	}
	if saved.Kind != "quote" || saved.Content != "值得存进笔记的结论" {
		t.Fatalf("unexpected excerpt entry: %+v", saved)
	}
	var source string
	if err := database.Pool.QueryRow(ctx, `
		SELECT br.props->>'source_block_revision_id'
		FROM content.block_revisions br WHERE br.id = $1::uuid
	`, saved.RevisionID).Scan(&source); err != nil || source != answer.BlockRevisionID {
		t.Fatalf("excerpt provenance: %v source=%s", err, source)
	}

	view, err := service.NoteView(ctx, principal, conversationID)
	if err != nil {
		t.Fatalf("note view: %v", err)
	}
	if !strings.Contains(view.DraftMarkdown, "值得存进笔记的结论") {
		t.Fatalf("saved excerpt must render into the note: %q", view.DraftMarkdown)
	}

	// The excerpt is note content: independently editable; the message it
	// came from stays immutable.
	if _, err := service.UpdateNoteBlock(ctx, principal, sepKey("edit"), conversationID, saved.BlockID, "改写后的结论"); err != nil {
		t.Fatalf("excerpt must be editable: %v", err)
	}
	if _, err := service.UpdateNoteBlock(ctx, principal, sepKey("editmsg"), conversationID, answer.BlockID, "改写消息"); err == nil {
		t.Fatal("message blocks must stay immutable")
	}

	// Validation: foreign revision 404s; excerpt body must not be client-set;
	// kind is restricted to quote/paragraph.
	if _, err := service.AddNoteBlock(ctx, principal, sepKey("f404"), conversationID, "", "", "00000000-0000-4000-8000-000000000000"); err == nil {
		t.Fatal("foreign revision must not be savable")
	}
	if _, err := service.AddNoteBlock(ctx, principal, sepKey("c422"), conversationID, "", "自带正文", answer.BlockRevisionID); err == nil {
		t.Fatal("client-set excerpt body must be rejected")
	}
	if _, err := service.AddNoteBlock(ctx, principal, sepKey("k422"), conversationID, "heading", "", answer.BlockRevisionID); err == nil {
		t.Fatal("heading excerpt kind must be rejected")
	}
}

// TestManualBlocksRenderClean freezes the note tree and proves the frozen
// markdown is clean prose: the renderer has no dialogue branch left.
func TestManualBlocksRenderClean(t *testing.T) {
	database := patternTestStore(t)
	service := Service{Store: database}
	ctx := context.Background()
	principal, conversationID, _ := patternFixture(t, database)

	if _, err := service.AddNoteBlock(ctx, principal, sepKey("h"), conversationID, "heading", "结论", ""); err != nil {
		t.Fatalf("add heading: %v", err)
	}
	if _, err := service.AddNoteBlock(ctx, principal, sepKey("p"), conversationID, "paragraph", "成文一段。", ""); err != nil {
		t.Fatalf("add paragraph: %v", err)
	}
	view, err := service.NoteView(ctx, principal, conversationID)
	if err != nil {
		t.Fatalf("note view: %v", err)
	}
	rendered := noteblocks.RenderMarkdown(noteblocks.Tree{})
	if strings.Contains(rendered, "##") {
		t.Fatal("empty tree must render empty")
	}
	for _, forbidden := range []string{"## User", "## Assistant", "## System", "## Tool"} {
		if strings.Contains(view.DraftMarkdown, forbidden) {
			t.Fatalf("frozen render must never carry dialogue headings (%s): %q", forbidden, view.DraftMarkdown)
		}
	}
	if !strings.Contains(view.DraftMarkdown, "成文一段。") {
		t.Fatalf("manual block missing from render: %q", view.DraftMarkdown)
	}
}
