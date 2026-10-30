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
	delete(result.Points, "scope")
	if err := validateEvaluation(&result, req); err == nil {
		t.Fatal("incomplete rubric accepted")
	}
}
