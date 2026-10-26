# Steering and stopping a request

While a request runs you can add to it, ask something else, or stop it. All of
it applies to the manager and to every subagent it started.

| Control | What happens |
| --- | --- |
| Steer | Your message reaches the manager and every running subagent at their next step, and subagents started later get it too. It is saved in the conversation. |
| Steer one subagent | From the subagent's run in Activity. Only that subagent gets it. |
| Ask on the side | A separate agent answers with the same tools and a snapshot of the conversation, without interrupting or joining the main request. |
| Queue | Runs after the current request. |
| Pause | Anything already under way (a model reply, a tool call) finishes; then every agent waits. Queued requests wait too. Resume carries on from the same point. |
| Stop | The request wraps up: the step under way finishes, no new tool calls start, and the manager answers with what it did and what is left. Subagents wrap up the same way. Queued requests then continue. |
| Stop everything | Cancels all running work now, tool calls included, and holds queued requests until you resume. |

Steps are checked before every model call and before every tool call, so
Pause and Stop take effect between tool calls, not only between replies.

API: `App.PauseRuns`, `App.ResumeRuns`, `App.StopCurrent`, `App.StopEverything`,
`App.SteerTask`, and `SubmitRoutedRequest(..., "steer", ...)`. The submission
state reports `paused`, `run_paused` and `stopping`.
