# Context

Every model has a context window: the most it can read in one request. NullBot
measures each request against the window of the model that will read it and
keeps it inside in three ways.

## How big the window is

NullBot looks up the selected model's window, in this order:

| Source | Where it comes from |
| --- | --- |
| Your override | Settings → Agent, under each model, saved as `compaction.context_windows["provider/model"]`. |
| Provider | The size the provider's model list reports: Anthropic (`max_input_tokens`), OpenRouter (`context_length`) and Codex (`context_window`). Saved in `models/context_windows.json`, so it is known offline and after a restart. OpenAI's model list doesn't report sizes. |
| Learned | A context-length error that names the real limit ("maximum context length is 128000 tokens"). Used for the rest of the request and saved for 7 days, then the provider's or family's size applies again. |
| Built-in | The documented size of the model family: Claude 200,000; GPT-5 272,000 (input); GPT-4.1 1,047,576; GPT-4o 128,000; o-series 200,000; Gemini 2.5 1,048,576. |
| Default | `compaction.approx_token_limit` (64,000), for a model nothing is known about. |

The main agent, planner and executor use the main model's window. Subagents use
the subagent model's.

## Measuring

Before each model call NullBot estimates the request: the system prompt,
skills, messages and every tool definition, at about four characters per token
(an image counts as about 1,600). After the call the provider reports how many
input tokens it actually counted, cache reads and writes included, and later
estimates in the request are scaled to match.

The percentages below are of the room for input: the window less what the
reply may use (the model's reply length limit, or about 5% of the window),
because providers that count the reply against the window reject a request
that leaves less than that.

The Activity panel shows the measurement on every model call, and the chat
shows how full the last request was.

## The three passes

| Pass | When | What happens |
| --- | --- | --- |
| Shorten | The request is 60% full (`compaction.shorten_ratio`), or has 80 messages (`message_count_limit`) | Tool results the model has already read become a digest: the tool, its arguments, the size of the result and its first 600 characters (`tool_digest_chars`). Long arguments of earlier tool calls are cut too. Skill text read with `skill_read` is never shortened. Oldest first, until the request is back to 45%. No model call. |
| Summarize | Still 85% full (`compaction.summarize_ratio`) after shortening | The model summarizes older messages. The most recent quarter of the window stays word for word. The summary carries the request being worked on, with its attachments and any guidance sent since, word for word (up to 15% of the window); skill text is kept in its own message. A later summary folds in the earlier one. |
| Guard | Every tool result, as it arrives | A result that wouldn't fit in the room left, counting what shortening could free, or that is more than half the window, is cut to its first 12,000 characters (`tool_result_char_limit`) with a notice giving its full size. The agent can then read it in smaller pieces: a line range, a search, a page of results. |

A context-length error from the provider runs Shorten and Summarize at once
and retries the call.

Turning off "Shorten and summarize automatically" stops both passes. The guard
still works, and then cuts results against the room actually left.

## Between requests

A new request carries earlier messages in the conversation, newest first, up to
30% of the window (`compaction.history_ratio`). When older messages don't fit,
the agent is told how many were left out and that `history_session_read` and
`history_search` can read them. Nothing is deleted: see [HISTORY.md](HISTORY.md).

**Compact** (the command palette, or `/compact [focus]`) runs the same
summarizing pass over the whole conversation now. The summary is saved in the
session as a `compaction` message, shown in the chat as a divider you can
expand. Later requests start from the summary; the messages before it stay in
the chat and on disk. If the model can't write the summary, nothing is saved
and you see why. A focus, such as "file paths and decisions", says what
the summary should keep in detail.

## Settings

Settings → Agent → Context:

| Setting | Config key | Default |
| --- | --- | --- |
| Shorten and summarize automatically | `compaction.enabled` | on |
| Shorten tool results at | `compaction.shorten_ratio` | 60% |
| Summarize at | `compaction.summarize_ratio` | 85% |
| Earlier messages per request | `compaction.history_ratio` | 30% |
| Characters kept from a shortened result | `compaction.tool_digest_chars` | 600 |
| Most of an oversized result shown | `compaction.tool_result_char_limit` | 12,000 |
| Size for an unknown model | `compaction.approx_token_limit` | 64,000 |
| What summaries should keep | `compaction.default_focus` | — |

A model's window can be overridden in Settings → Agent, under the manager and
subagent models; Settings → Models shows each model's window. `threshold_ratio`,
the former single threshold, becomes `summarize_ratio` if you had changed it.
