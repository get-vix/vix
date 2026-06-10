The goal run is ending. Do not start new substantive work.

Run state:
- Workflow status: $(workflow.status)
- Declared signal: $(workflow.signal.status) $(workflow.signal.note)
- Iterations: $(workflow.iteration)
- Tokens used: $(workflow.tokens_used)
- Time elapsed: $(workflow.elapsed_seconds) seconds

Write a concise wrap-up for the user:
1. What was accomplished, with the key evidence (files changed, tests passing, behavior verified).
2. If the goal is not complete: exactly what remains, and what is blocking it (if anything).
3. The single best next step for the user.
4. One line of resource usage drawn from the run state above.

Be honest about gaps — do not present partial progress as completion.
