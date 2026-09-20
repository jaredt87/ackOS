	gitTest(t, target.Repository, "commit", "--allow-empty", "-m", "merge side")
	gitTest(t, target.Repository, "checkout", "-")
	gitTest(t, target.Repository, "commit", "--allow-empty", "-m", "local side")
	gitTest(t, target.Repository, "merge", "--no-commit", "merge-test")
	transition := kernel.Transition{Subject: target.Subject, Before: observation.State, After: "updated"}
	authority := kernel.Authority{ExecutionID: "attempt-verifier-pending-merge"}
	if _, err := target.lifecycle.capture(context.Background(), target, authority.ExecutionID); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.Verify(context.Background(), transition, authority); err == nil || !strings.Contains(err.Error(), "MERGE_HEAD") {
		t.Fatalf("verifier error = %v, want pending-merge rejection", err)
	}
	_ = executor
	gitTest(t, target.Repository, "merge", "--abort")
}

func TestExecutorRejectsPendingGitMerge(t *testing.T) {
	target, observer, executor, _, _ := newTestProvider(t, "initial")
	observation, err := observer.Observe(context.Background(), target.Subject)