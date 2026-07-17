package deliberate

import (
	"errors"
	"testing"
)

// The table is deliberately heavy on the ways a model gets this slightly wrong,
// because that is what it will do. Every ambiguous case must land on FAIL.
func TestParseVerdict(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		wantPass bool
		wantN    int
		wantErr  error
		why      string
	}{
		{
			name:     "clean pass",
			out:      "I attacked it and found nothing.\n\nVERDICT: PASS\n",
			wantPass: true,
			why:      "the only path that ends a run",
		},
		{
			name: "fail with findings",
			out: "```json\n{\"verdict\":\"FAIL\",\"findings\":[" +
				"{\"category\":\"edge-case\",\"severity\":\"high\",\"summary\":\"s\",\"failure\":\"f\",\"where\":\"§4\"}" +
				"]}\n```\n\nVERDICT: FAIL\n",
			wantPass: false,
			wantN:    1,
		},
		{
			name:     "PASS but it listed findings anyway",
			out:      "```json\n{\"verdict\":\"PASS\",\"findings\":[{\"summary\":\"actually this breaks\"}]}\n```\n\nVERDICT: PASS\n",
			wantPass: false,
			wantN:    1,
			why:      "the two statements contradict; continuing costs a round, stopping costs the guarantee",
		},
		{
			name:     "FAIL with no findings",
			out:      "VERDICT: FAIL\n",
			wantPass: false,
			why:      "a wasted round, not a wrong one — it said the spec is not done",
		},
		{
			name:    "no sentinel at all",
			out:     "Looks fine to me!",
			wantErr: ErrNoVerdict,
			why:     "caller re-prompts once, then FAILs — never passes",
		},
		{
			name:     "restates the format then answers",
			out:      "I was told to end with VERDICT: PASS or VERDICT: FAIL.\nHere are my findings.\nVERDICT: FAIL\n",
			wantPass: false,
			why:      "the LAST sentinel is the answer; the first is the model quoting its instructions",
		},
		{
			name:     "lowercase and padded",
			out:      "  verdict:   pass  \n",
			wantPass: true,
			why:      "formatting slop must not read as a flaw, nor as an error",
		},
		{
			name:     "bare array instead of the documented object",
			out:      "```json\n[{\"summary\":\"a\"},{\"summary\":\"b\"}]\n```\nVERDICT: FAIL\n",
			wantPass: false,
			wantN:    2,
			why:      "models produce both shapes; the difference is not worth a round",
		},
		{
			name:     "prose mentioning the word pass",
			out:      "This does not pass muster.\nVERDICT: FAIL\n",
			wantPass: false,
			why:      "only the sentinel line counts, never prose",
		},
		{
			name:     "malformed json, valid sentinel",
			out:      "```json\n{oh no\n```\nVERDICT: FAIL\n",
			wantPass: false,
			wantN:    0,
			why:      "an unreadable critique is still a FAIL; the raw text goes to the next planner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := ParseVerdict(tt.out)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if v.Pass != tt.wantPass {
				t.Errorf("Pass = %v, want %v — %s", v.Pass, tt.wantPass, tt.why)
			}
			if len(v.Findings) != tt.wantN {
				t.Errorf("findings = %d, want %d", len(v.Findings), tt.wantN)
			}
			if v.Raw != tt.out {
				t.Error("Raw must preserve the turn verbatim")
			}
		})
	}
}

// TestNothingButAnExplicitPassCanPass is the property the whole design rests on,
// asserted directly rather than left implied by the table above. If this test
// ever fails, the pipeline can ship a specification that nothing attacked.
func TestNothingButAnExplicitPassCanPass(t *testing.T) {
	neverPass := []string{
		"",
		"looks good",
		"VERDICT: MAYBE",
		"VERDICT:PASS extra words after it",
		"the spec passes review",
		"```json\n{\"verdict\":\"PASS\"}\n```",
	}
	for _, out := range neverPass {
		v, err := ParseVerdict(out)
		if err == nil && v.Pass {
			t.Errorf("output %q was read as PASS; only a well-formed VERDICT: PASS line may pass", out)
		}
	}
}
