# PR descriptions

**Keep PR descriptions short and on-subject.** Use this shape:

```
<Tracker>: [<ID>](<url>)

One short paragraph: what was broken or missing, and why.

- 3-5 bullets on what changed, naming classes, jobs, functions, and key numbers.

Follow-up: [<ID>](<url>)
```

- The first line is the ticket link (`Linear:`, `Jira:`, `Issue:`). Omit it when there is no ticket.
- Add the `Follow-up:` line only when work was split out to another ticket.
- No headed sections (Problem / Change / Testing). No test-case lists. Do not restate the diff.
- Review-round history goes in PR comments, not the body.
