# PR Review Loop

This is the canonical workflow for reviewing and advancing pull requests.

1. **Gate review requests and merges on the current PR HEAD and green CI.**
   - Treat the current PR HEAD as authoritative.
   - Do not act on stale review state or stale commits.
   - Diagnostic work and fixes are allowed when CI is red; restore green CI before requesting another review or merging.

2. **Treat one Codex review as one complete review batch.**
   - Collect **all** findings from the review before taking action.
   - Classify every finding as:
     - **Fix** — valid and in scope; fix it on the PR branch.
     - **Accept** — valid but out of scope or otherwise intentionally not fixed; document the reason in the PR.
     - **Needs clarification** — the finding cannot yet be classified because its intent, correctness, or scope is unclear.

3. **Needs clarification is temporary and blocking.**
   - It is never a final disposition.
   - Resolve the clarification before treating that finding as accepted or fixed.
   - Do not advance the review loop while a finding remains in this state.

4. **Fix each valid in-scope batch coherently on the same branch.**
   - Make the necessary fixes together rather than responding piecemeal to individual findings.
   - Run the required checks and get CI green again.
   - The next review must be against the resulting current HEAD.

5. **After the batch changes the PR HEAD, request exactly one new review.**
   - Do not request another review against the same HEAD.
   - After the fixes are pushed and required CI is green, confirm the PR HEAD changed from the reviewed HEAD.
   - Then request **exactly one** new `@codex` review.

6. **Never review a stale HEAD.**
   - Every review request must target the current PR HEAD.
   - Reconfirm the PR HEAD and CI state before acting on review results.

7. **Never manually resolve review threads.**
   - Let GitHub's review state reflect the actual lifecycle of the findings.
   - Do not resolve threads merely to make the PR appear clean.

8. **Never manufacture changes for hypothetical concerns.**
   - Do not add code, tests, refactors, or other changes solely to preempt concerns that have not been raised and are not required by the current task or repository contract.

9. **Document conscious out-of-scope acceptance.**
   - A valid finding that is intentionally out of scope may be accepted.
   - Record the finding and the specific reason it is deferred or excluded in the PR so the decision is explicit and reviewable.

The loop ends only when the current PR HEAD has green CI and the complete current review state has no unresolved, valid in-scope findings.
