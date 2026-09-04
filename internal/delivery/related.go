package delivery

// related.go — the detail page's related-posts block (G7). Candidates come
// from the unified query service via PublicReader.Search in fulltext mode
// (title + summary OR query, the same recall shape relation_candidates
// uses); the vector-recall variant stays a future enhancement (plan §15.3 —
// it needs a chunk-embedding readback path in the query package). Fallback
// chain: fulltext → same first tag → latest. The block is fail-open: any
// error or empty tier degrades silently to the next tier, never to a 500.

import (
	"context"
	"strings"

	"agentchunzhi/internal/auth"
	"agentchunzhi/internal/site"
)

const relatedMaxItems = 3

// attachDetailRelated resolves and attaches the related block to the detail
// VM. It runs inside the page build (cold path only — the whole page,
// related block included, sits in the page cache afterwards).
func (s *Service) attachDetailRelated(ctx context.Context, addr string, principal auth.Principal, slug string, content site.PublicPostContent, vm *DetailVM) {
	cards := s.relatedCandidates(ctx, addr, principal, slug, content)
	if len(cards) > relatedMaxItems {
		cards = cards[:relatedMaxItems]
	}
	vm.Related = cards
}

// relatedCandidates walks the fallback chain and deduplicates against the
// post itself. Every tier failure degrades to the next tier.
func (s *Service) relatedCandidates(ctx context.Context, addr string, principal auth.Principal, slug string, content site.PublicPostContent) []CardVM {
	cards := make([]CardVM, 0, relatedMaxItems)
	seen := map[string]bool{content.AssetID: true}
	add := func(page site.PublicPostPage) {
		for _, post := range page.Items {
			if len(cards) >= relatedMaxItems {
				return
			}
			if seen[post.AssetID] || post.Title == "" {
				continue
			}
			seen[post.AssetID] = true
			cards = append(cards, cardVM(slug, post, 120))
		}
	}
	// Tier 1: fulltext recall over the title (+summary head) through the
	// unified query service — the same visibility scope as the page itself.
	if query := relatedQuery(content); query != "" {
		if page, err := s.Reader.Search(ctx, addr, principal, slug, query, "fulltext", site.PublicPostQuery{Limit: relatedMaxItems + 3}); err == nil {
			add(page)
		}
	}
	if len(cards) >= relatedMaxItems {
		return cards
	}
	// Tier 2: newest posts sharing the first tag.
	for _, tag := range content.Tags {
		if page, err := s.Reader.TagPage(ctx, addr, principal, slug, tag.Key, site.PublicPostQuery{Limit: relatedMaxItems + 3}); err == nil {
			add(page)
		}
		break
	}
	if len(cards) >= relatedMaxItems {
		return cards
	}
	// Tier 3: site-latest.
	if page, err := s.Reader.Posts(ctx, addr, principal, slug, site.PublicPostQuery{Limit: relatedMaxItems + 3}); err == nil {
		add(page)
	}
	return cards
}

// relatedQuery builds the OR query for tier 1: up to 8 significant title
// terms (plus the summary head), mirroring the relation-candidate recall
// shape without importing the automation package.
func relatedQuery(content site.PublicPostContent) string {
	text := strings.TrimSpace(content.Title)
	if text == "" {
		return ""
	}
	if summary := strings.TrimSpace(content.Summary); summary != "" {
		runes := []rune(summary)
		if len(runes) > 200 {
			summary = string(runes[:200])
		}
		text = text + "\n" + summary
	}
	terms := relatedTerms(text, 8)
	if len(terms) == 0 {
		return ""
	}
	return strings.Join(terms, " OR ")
}

// relatedTerms splits text into escaped PGroonga terms: CJK bigrams plus
// ASCII word runs, deduplicated, capped.
func relatedTerms(text string, limit int) []string {
	seen := map[string]bool{}
	terms := []string{}
	runes := []rune(text)
	addTerm := func(term string) {
		term = strings.TrimSpace(term)
		if term == "" || seen[term] || len(terms) >= limit {
			return
		}
		seen[term] = true
		terms = append(terms, escapePGroonga(term))
	}
	var word []rune
	flushWord := func() {
		if len(word) > 1 {
			addTerm(string(word))
		}
		word = word[:0]
	}
	for i, ch := range runes {
		switch {
		case isASCIIWord(rune(ch)):
			word = append(word, rune(ch))
		case isCJK(rune(ch)):
			flushWord()
			if i+1 < len(runes) && isCJK(runes[i+1]) {
				addTerm(string(runes[i : i+2]))
			}
		default:
			flushWord()
		}
	}
	flushWord()
	return terms
}

func isASCIIWord(ch rune) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

func isCJK(ch rune) bool {
	return ch >= 0x4e00 && ch <= 0x9fff || ch >= 0x3040 && ch <= 0x30ff
}

// escapePGroonga quotes one term for the fulltext query syntax.
func escapePGroonga(term string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + replacer.Replace(term) + `"`
}
