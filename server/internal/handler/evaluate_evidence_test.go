package handler

import "testing"

func TestEvaluationRequiresEvidenceAndComputesTotals(t *testing.T) {
	score := 4
	result := evaluateResponse{Points: map[string]pointDetail{
		"goal":    {Score: &score, Evidence: []string{"Fix the failing test"}},
		"context": {}, "scope": {}, "verification": {},
	}}
	req := evaluateRequest{Prompt: "Fix the failing test"}
	if err := validateEvaluation(&result, req); err != nil {
		t.Fatal(err)
	}
	if result.Total == nil || *result.Total != 4 || result.MaxTotal != 5 {
		t.Fatalf("incorrect denominator: %+v", result)
	}
	point := result.Points["goal"]
	point.Evidence = []string{"invented source"}
	result.Points["goal"] = point
	if err := validateEvaluation(&result, req); err == nil {
		t.Fatal("fabricated evidence accepted")
	}
	point.Evidence = []string{req.Prompt}
	score = 6
	result.Points["goal"] = point
	if err := validateEvaluation(&result, req); err == nil {
		t.Fatal("out-of-range score accepted")
	}
	result.Points["goal"] = pointDetail{}
	if err := validateEvaluation(&result, req); err != nil {
		t.Fatal(err)
	}
	if result.Total != nil || result.MaxTotal != 0 {
		t.Fatal("abstention became a zero score")
	}
	result.Points["goal"] = pointDetail{Evidence: []string{"fabricated even when unscored"}}
	if err := validateEvaluation(&result, req); err == nil {
		t.Fatal("abstention accepted fabricated evidence")
	}
	result.Points["goal"] = pointDetail{}
	delete(result.Points, "scope")
	if err := validateEvaluation(&result, req); err == nil {
		t.Fatal("incomplete rubric accepted")
	}
}

func TestEvaluationEvidencePreservesWordsAndMessageBoundaries(t *testing.T) {
	sources := []string{"Fix\n  the\t test", "Use “strict” mode", "first fragment", "second fragment"}
	for _, tc := range []struct {
		quote string
		want  bool
	}{
		{"Fix the test", true}, {"Fix the tests", false}, {"Use \"strict\" mode", false},
		{"fragment second", false}, {" \n \t", false},
	} {
		if got := containsEvidence(sources, tc.quote); got != tc.want {
			t.Errorf("quote %q: got %v", tc.quote, got)
		}
	}
}

func TestEvaluationAdviceRequiresSourceEvidence(t *testing.T) {
	result := evaluateResponse{Points: map[string]pointDetail{"goal": {}, "context": {}, "scope": {}, "verification": {}}, Suggestions: []evaluationSuggestion{{Target: "execution", Evidence: []string{"Run the tests", "I skipped testing"}, Impact: "The requested verification was skipped.", Action: "Run the requested tests."}}}
	req := evaluateRequest{Prompt: "Run the tests", Context: evaluateContext{Excerpts: []evaluationExcerpt{{Role: "assistant", Content: "I skipped testing"}}}}
	if err := validateEvaluation(&result, req); err != nil {
		t.Fatal(err)
	}
	result.Suggestions[0].Evidence = []string{"The tests passed"}
	if err := validateEvaluation(&result, req); err == nil {
		t.Fatal("invented result accepted")
	}
	result.Suggestions[0].Evidence = []string{req.Prompt}
	result.Suggestions[0].Action = ""
	if err := validateEvaluation(&result, req); err == nil {
		t.Fatal("non-actionable advice accepted")
	}
}
