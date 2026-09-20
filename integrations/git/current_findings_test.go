		if len(args) >= 3 && args[0] == "--no-replace-objects" && args[1] == "log" && args[2] == "-1" {
			return "ackOS: execute wrong-marker", nil
		}
		return runGit(context.Background(), target.Repository, args...)
	}
	err = verifyCommitAt(context.Background(), target, git, parent, transition, "marker-test")
	if err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("error = %v, want captured-commit marker verification failure", err)
	}
}

func TestVerifierRejectsCommitWithWrongPreExecutionParent(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	capturedParent := target.capturedHead
	if _, err := target.lifecycle.capture(context.Background(), target, "wrong-parent"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(target.Repository, "other.txt"), []byte("unrelated"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", "other.txt")
	gitTest(t, target.Repository, "commit", "-m", "advance history")

	afterHash, err := runGitInput(context.Background(), target.Repository, []byte("updated"), "hash-object", "-w", "--no-filters", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	afterHash = strings.TrimSpace(afterHash)
	gitTest(t, target.Repository, "write-tree")
	gitTest(t, target.Repository, "update-index", "--add", "--cacheinfo", "100644,"+afterHash+","+target.Path)
	tree := strings.TrimSpace(gitTest(t, target.Repository, "write-tree"))
	parent := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "HEAD"))
	commit := strings.TrimSpace(gitTest(t, target.Repository, "commit-tree", tree, "-p", parent, "-m", "ackOS: execute wrong-parent"))
	gitTest(t, target.Repository, "update-ref", "HEAD", commit)
	if err := os.WriteFile(filepath.Join(target.Repository, target.Path), []byte("updated"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, target.Repository, "add", "--", target.Path)

	verifier := Verifier{Target: target}
	transition := kernel.Transition{Subject: target.Subject, Before: "initial", After: "updated"}
	_, err = verifier.Verify(context.Background(), transition, kernel.Authority{ExecutionID: "wrong-parent"})
	if err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("error = %v, want captured pre-execution parent rejection (captured %s)", err, capturedParent)
	}
}

func TestVerifierRejectsDetachedHead(t *testing.T) {
	target, _, _, _, _ := newTestProvider(t, "initial")
	parent := target.capturedHead
	if _, err := target.lifecycle.capture(context.Background(), target, "detached-test"); err != nil {
		t.Fatal(err)
	}
	afterHash, err := gitBlobHash(context.Background(), target, "updated")
	if err != nil {
		t.Fatal(err)
	}
	if err := commitVerifiedTree(context.Background(), target, parent, target.capturedHeadRef, afterHash, []byte("updated"), "ackOS: execute detached-test"); err != nil {
		t.Fatal(err)
	}
	commit := strings.TrimSpace(gitTest(t, target.Repository, "rev-parse", "HEAD"))
	gitTest(t, target.Repository, "checkout", "--detach", commit)

	verifier := Verifier{Target: target}
	transition := kernel.Transition{Subject: target.Subject, Before: "initial", After: "updated"}
	_, err = verifier.Verify(context.Background(), transition, kernel.Authority{ExecutionID: "detached-test"})
	if err == nil || !strings.Contains(err.Error(), "authorized branch") {
		t.Fatalf("error = %v, want authorized-branch rejection", err)
	}
}