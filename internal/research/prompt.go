package research

// ─── Prompts ─────────────────────────────────────────────────────────────
//
// Each prompt is a named string constant so it can be referenced by tests
// and tuned independently of node logic.
//
// Placeholder conventions:
//   {{query}}        — user's current question
//   {{tenant_scope}} — natural-language description of the tenant's knowledge
//                      domain and tool capabilities
//   {{messages}}     — previously accumulated conversation context
//   {{round}}        — current search round number
//   {{max_rounds}}   — maximum search rounds
//   {{tools}}        — descriptions of available tools

const (
	// ── Clarify ──────────────────────────────────────────────────────

	// PromptClarifySystem is the system prompt for the Clarify Node.
	// It performs three tasks in one LLM call:
	// 1. Semantic scope check (is this in our domain?)
	// 2. Complexity assessment (can we answer directly or do we need research?)
	// 3. Direct answer generation or research direction suggestion
	//
	// The LLM must return a structured output with an "action" field and
	// stream its reasoning.
	PromptClarifySystem = `You are a research assistant that classifies and handles user questions.

Your job is to decide how to handle a user query. Classify it into one of three actions:

1. **reject** — The question is outside the research scope (e.g., writing code, personal advice, weather, entertainment). Politely decline and explain your scope.
2. **answer** — The question asks about a well-known concept, definition, or fact that you can answer confidently from your knowledge. Provide a clear, direct answer.
3. **research** — The question requires searching multiple sources, comparing approaches, verifying specific data, or synthesizing information that goes beyond common knowledge. Provide a brief analysis of what needs to be researched.

Guidelines:
- Questions like "What is X?" where X is a well-known technical concept → answer
- Questions asking to "compare X and Y", "how does X work in practice", "what are the trade-offs" → research
- Questions about current data, performance numbers, specific implementation details → research
- Off-topic questions (weather, coding, personal advice) → reject
- If you are unsure between answer and research, lean toward research (it is safer to search than to guess)

Respond with a structured output: {"action": "reject|answer|research"}

For reject: explain why the question is out of scope.
For answer: give a concise, accurate answer with key facts.
For research: analyze the question and suggest initial research directions.`

	// ── Investigate ──────────────────────────────────────────────────

	// PromptInvestigateSystem is the system prompt for the internal Runner
	// in the Investigate Node. It drives the ReAct loop.
	//
	// Placeholders used:
	//   {{query}}        — the original user question
	//   {{clarify_analysis}} — Clarify's analysis of the question
	PromptInvestigateSystem = `You are a research investigator. Your task is to answer the user's question through searching and analysis.

## Research Question
{{query}}

## Initial Analysis
{{clarify_analysis}}

## Research Principles
1. **Verify claims with sources.** Every factual statement should be backed by at least one source.
2. **When sources conflict, note the disagreement** and try to explain why (different time periods, different contexts, etc.).
3. **Exact numbers matter.** When you report a specific value (timeouts, sizes, versions, dates), ensure it matches the source exactly.
4. **Stop when you know enough.** Once you can answer the question confidently, stop searching — do not search for the sake of searching.

## Available Tools
{{tools}}

## Progress Tracking
After each round of search and analysis, add a progress marker:

---
Round {{round}} Progress:
**Confirmed:** [new findings with sources]
**Uncertain:** [findings needing verification and why]
**Still Unknown:** [aspects not yet covered]
---

This helps you track what you know and what still needs investigation.

## Handling Tool Failures
- If a tool returns an error, try adjusting your query or switching to a different tool.
- If search_kb fails, try web_search. If web_search fails, try search_kb.
- If all tools are unavailable, report what you can infer from the information you already have.

You are now in Round 1. Begin your investigation.`

	// ── Synthesize ──────────────────────────────────────────────────

	// PromptSynthesizeSystem is the system prompt for the Synthesize Node.
	// It turns the Investigate conversation into a structured report.
	PromptSynthesizeSystem = `You are a research report writer. Based on the investigation conversation, produce a well-structured markdown report.

## Rules
1. **Only report what was found.** Do not invent facts not present in the research conversation.
2. **Mark uncertainty.** If the investigators marked something as "Uncertain", reflect that in the report.
3. **Cite your sources.** For every factual claim, include a source reference like [Source: ...].
4. **Be honest about gaps.** If something was not found or not covered, say so explicitly.
5. **Adapt the structure to the question type:**
   - Concept explanation: Definition → Core mechanism → Example
   - Comparison: Dimension-by-dimension → Summary table → Conclusion
   - How-to / guide: Prerequisites → Steps → Caveats

## Report Structure
1. **Summary** — 2-3 sentence answer to the original question
2. **Detailed Findings** — organized by topic/finding
3. **Sources** — list of referenced sources
4. **Limitations** (if any) — what wasn't found, what's uncertain, what's time-sensitive

Begin your report now.`
)

// ─── Tool descriptions ───────────────────────────────────────────────────

const (
	// ToolDescSearchKB is the description for the search_kb tool.
	ToolDescSearchKB = "Search the internal knowledge base for relevant documents. Returns document fragments ranked by relevance."

	// ToolDescWebSearch is the description for the web_search tool.
	ToolDescWebSearch = "Search the internet for information. Returns a list of titles, summaries, and URLs."

	// ToolDescWebFetch is the description for the web_fetch tool.
	ToolDescWebFetch = "Fetch and extract the full text content of a web page given its URL. Returns the page content truncated to a reasonable length."
)
