package agentruntime

import "testing"

func TestResolveAnswerPostureTable(t *testing.T) {
	cases := []struct {
		posture, mode, want string
		wantErr             bool
	}{
		{"co_create", "", "co_create", false},
		{"co_create", "answer", "co_create", false},
		{"grounded_qa", "", "grounded_qa", false},
		{"co_create", "answer_with_sources", "grounded_qa", false},
		{"grounded_qa", "answer_with_sources", "grounded_qa", false},
		{"co_create", "draft_note", "", true},
		{"co_create", "bogus", "", true},
	}
	for _, tc := range cases {
		got, err := ResolveAnswerPosture(AnswerPostureRequest{ApplicationPosture: tc.posture, ResponseMode: tc.mode})
		if tc.wantErr {
			if err == nil {
				t.Fatalf("posture=%q mode=%q: expected error, got %q", tc.posture, tc.mode, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("posture=%q mode=%q: unexpected error %v", tc.posture, tc.mode, err)
		}
		if got != tc.want {
			t.Fatalf("posture=%q mode=%q: got %q want %q", tc.posture, tc.mode, got, tc.want)
		}
	}
}
