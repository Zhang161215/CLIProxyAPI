// Package content is the pre-upstream content gate for the Claude path.
//
// A refusal from the upstream safeguards team ends the account; a rate limit
// does not. This package is the only layer in the fork that can prevent that
// outcome, so it runs before a request is scheduled and never spends upstream
// quota when it blocks.
//
// Scope is deliberately narrow. The gate looks at the latest user-authored text
// only: transcript history is scanned by nothing, tool output is skipped, and
// system prompts are ignored. A gate that inspects everything blocks ordinary
// work, and a gate that blocks ordinary work gets switched off.
package content

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"
)

// Category groups rules by the failure they prevent.
type Category string

const (
	// CategoryDistill covers attempts to harvest reasoning traces, verbatim
	// system prompts, or benchmark answers.
	CategoryDistill Category = "distill"
	// CategoryJailbreak covers attempts to remove safety behaviour.
	CategoryJailbreak Category = "jailbreak"
	// CategoryNSFW covers explicitly sexual requests.
	CategoryNSFW Category = "nsfw"
)

// Rule is one compiled pattern.
type Rule struct {
	ID       string
	Category Category
	Pattern  *regexp.Regexp
}

// Verdict is the outcome of scanning one request.
type Verdict struct {
	Blocked  bool
	RuleID   string
	Category Category
	// Excerpt is a short window around the match for audit logs. It is
	// truncated so a log never becomes a copy of the user's prompt.
	Excerpt string
}

// builtinPatterns are intentionally few and high signal. Every pattern here
// must be defensible as "this is not ordinary coding work", because a false
// positive is a user who cannot use the product.
var builtinPatterns = []struct {
	id       string
	category Category
	pattern  string
}{
	{"distill.system_prompt", CategoryDistill, `(?i)(reveal|show|print|repeat|output|leak)\b.{0,24}\b(system prompt|system message|hidden (?:prompt|instructions)|initial instructions)\b`},
	{"distill.instructions_dump", CategoryDistill, `(?i)(what|which)\b.{0,32}\b(system prompt|system message|instructions)\b.{0,24}\b(are|were|do) you\b`},
	{"distill.ignore_previous", CategoryDistill, `(?i)ignore\b.{0,20}\b(all )?(the )?(previous|prior|above|earlier) (instructions|rules|prompts)\b`},
	{"distill.reasoning_dump", CategoryDistill, `(?i)(full|entire|complete|detailed|step[- ]by[- ]step)\b.{0,24}\b(chain[- ]of[- ]thought|reasoning (?:trace|process|steps))\b`},
	// The noun carries the signal: 系统提示词 / 隐藏指令, never a bare 提示词.
	// 提示词 is ordinary vocabulary in Chinese LLM work (write the prompt, tune
	// the template, compare prompts), so matching it bare turned normal
	// documentation into a violation. The window is wider than the English rule
	// on purpose: with the noun pinned, a longer reach catches more attempts
	// without catching ordinary prose.
	{"distill.zh.prompt", CategoryDistill, `(输出|打印|复述|展示|泄露|给我看|发给我|告诉我).{0,16}(你的|您的|当前|原始|完整的?)?(系统提示词|系统提示|系统消息|系统指令|隐藏指令|隐藏提示词)`},
	{"distill.zh.ignore", CategoryDistill, `忽略(之前|以上|上述|前面).{0,8}(的)?(所有)?(指令|指示|规则|限制)`},
	{"distill.zh.reasoning", CategoryDistill, `(完整|详细|逐步).{0,10}(输出|展示|写出|给出).{0,10}(思维链|推理过程|思考过程)`},
	{"distill.zh.prompt_after", CategoryDistill, `(系统提示词|系统提示|系统消息|系统指令|隐藏指令|隐藏提示词).{0,16}(完整|详细|原样)?.{0,6}(输出|打印|复述|展示|泄露|给我看|发给我|告诉我)`},
	{"distill.zh.reasoning_after", CategoryDistill, `(思维链|推理过程|思考过程).{0,10}(完整|详细|逐步).{0,10}(输出|展示|写出|给出)`},
	{"jailbreak.dan", CategoryJailbreak, `(?i)\b(DAN|do anything now)\b`},
	{"jailbreak.persona", CategoryJailbreak, `(?i)(act|behave|respond) as (?:an? )?(unrestricted|uncensored|unfiltered|evil|amoral)\b`},
	{"jailbreak.disable_safety", CategoryJailbreak, `(?i)(disable|bypass|turn off|remove)\b.{0,20}\b(safety|guardrails|content (?:filter|policy)|restrictions)\b`},
	{"jailbreak.zh", CategoryJailbreak, `(越狱|绕过|解除|关闭|去除).{0,8}(限制|安全|审查|审核|过滤)`},
	{"nsfw.explicit", CategoryNSFW, `(?i)\b(explicit sexual|pornographic|erotic (?:story|scene)|write (?:me )?(?:a )?sex scene)\b`},
	{"nsfw.zh", CategoryNSFW, `(写|来|生成).{0,6}(色情|黄文|露骨|性行为)`},
}

// activeRules is the compiled set: builtins plus any operator-supplied rules.
// It is replaced atomically on reload so a request never sees a half-built set.
var (
	rulesMu     sync.RWMutex
	activeRules = compileRules()
	baseRules   = activeRules
)

// DisableEnv lists rule IDs to skip, comma separated.
//
// A single over-eager rule used to leave the operator with one option: turn the
// whole gate off and lose every other rule with it. Naming the rule is the
// smaller hammer.
const DisableEnv = "CPA_ANTIDETECT_CONTENT_GATE_DISABLE"

func compileRules() []Rule {
	compiled := make([]Rule, 0, len(builtinPatterns))
	for _, entry := range builtinPatterns {
		compiled = append(compiled, Rule{
			ID:       entry.id,
			Category: entry.category,
			Pattern:  regexp.MustCompile(entry.pattern),
		})
	}
	return filterDisabled(compiled)
}

// filterDisabled drops rules named in DisableEnv.
func filterDisabled(rules []Rule) []Rule {
	disabled := disabledRuleIDs()
	if len(disabled) == 0 {
		return rules
	}
	kept := make([]Rule, 0, len(rules))
	for _, rule := range rules {
		if disabled[rule.ID] {
			continue
		}
		kept = append(kept, rule)
	}
	return kept
}

func disabledRuleIDs() map[string]bool {
	raw := strings.TrimSpace(os.Getenv(DisableEnv))
	if raw == "" {
		return nil
	}
	disabled := make(map[string]bool, 4)
	for _, part := range strings.Split(raw, ",") {
		if id := strings.TrimSpace(part); id != "" {
			disabled[id] = true
		}
	}
	return disabled
}

// Rules returns the active rule set. Callers must not mutate it.
func Rules() []Rule {
	rulesMu.RLock()
	defer rulesMu.RUnlock()
	return activeRules
}

// SetExtraRules installs operator-supplied rules on top of the builtins.
//
// Reloading is a replacement, not a merge with the previous extra set: an
// operator who removes a rule expects it to stop matching.
func SetExtraRules(extra []Rule) {
	combined := make([]Rule, 0, len(baseRules)+len(extra))
	combined = append(combined, baseRules...)
	combined = append(combined, extra...)

	rulesMu.Lock()
	activeRules = filterDisabled(combined)
	rulesMu.Unlock()
}

// Scan evaluates one text against every rule and returns the first match.
func Scan(text string) Verdict {
	text = scanScope(text)
	if strings.TrimSpace(text) == "" {
		return Verdict{}
	}
	for _, rule := range Rules() {
		loc := rule.Pattern.FindStringIndex(text)
		if loc == nil {
			continue
		}
		return Verdict{
			Blocked:  true,
			RuleID:   rule.ID,
			Category: rule.Category,
			Excerpt:  excerpt(text, loc[0], loc[1]),
		}
	}
	return Verdict{}
}

const (
	// scanChunkBytes is how much of each end of a very long text is searched.
	scanChunkBytes = 16 * 1024
	// scanMaxBytes is past which a text is searched by its ends only.
	scanMaxBytes = 32 * 1024
)

// machineWrapped matches text a client assembles rather than a person types.
//
// The security-review subagent wraps the entire prior conversation in a
// <transcript> block and sends it as a user message; a 130k-character document
// then gets scanned end to end, so the chance that some rule fires somewhere
// grows with document length instead of with intent. The block is the review
// subject, not an instruction.
var machineWrapped = regexp.MustCompile(`(?is)<transcript>.*?</transcript>`)

// scanScope reduces a text to what a person actually wrote.
//
// Long inputs are searched at both ends rather than in full: an instruction to
// leak something sits at the start or the end of a message, never 100 KB into a
// pasted file, and scanning everything made cost and false positives grow with
// paste size.
func scanScope(text string) string {
	if strings.Contains(text, "transcript>") {
		text = machineWrapped.ReplaceAllString(text, " ")
	}
	if len(text) <= scanMaxBytes {
		return text
	}
	head := text[:scanChunkBytes]
	for len(head) > 0 && !utf8.RuneStart(text[len(head)]) {
		head = head[:len(head)-1]
	}
	tailStart := len(text) - scanChunkBytes
	for tailStart < len(text) && !utf8.RuneStart(text[tailStart]) {
		tailStart++
	}
	return head + "\n" + text[tailStart:]
}

// excerpt returns a bounded window around the match so audit logs stay small.
func excerpt(text string, start, end int) string {
	const window = 24
	from := start - window
	if from < 0 {
		from = 0
	}
	to := end + window
	if to > len(text) {
		to = len(text)
	}
	return strings.Join(strings.Fields(text[from:to]), " ")
}

// Evaluate scans the latest user-authored text of a Messages payload.
//
// It returns an empty verdict when the payload cannot be parsed: a malformed
// body is an upstream problem, not a content problem, and blocking it here
// would hide the real error.
func Evaluate(payload []byte) Verdict {
	return Scan(LatestUserText(payload))
}

// LatestUserText extracts the newest user text from a Messages payload.
//
// Tool results are skipped: they are model- or machine-authored, they routinely
// contain file contents and command output, and scanning them blocks legitimate
// work. Only the last user turn is considered; earlier turns were already
// scanned when they were the newest.
func LatestUserText(payload []byte) string {
	if text := latestMessageText(payload); text != "" {
		return text
	}
	// The Responses API carries the conversation under "input" instead of
	// "messages". Without this branch the gate is bypassed entirely by any
	// client using that format.
	return latestInputText(payload)
}

func latestMessageText(payload []byte) string {
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil {
		return ""
	}
	for index := len(body.Messages) - 1; index >= 0; index-- {
		message := body.Messages[index]
		if message.Role != "user" {
			continue
		}
		text := textFromContent(message.Content)
		if strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func latestInputText(payload []byte) string {
	var body struct {
		Input json.RawMessage `json:"input"`
	}
	if errUnmarshal := json.Unmarshal(payload, &body); errUnmarshal != nil || len(body.Input) == 0 {
		return ""
	}

	var plain string
	if errPlain := json.Unmarshal(body.Input, &plain); errPlain == nil {
		return plain
	}

	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if errItems := json.Unmarshal(body.Input, &items); errItems != nil {
		return ""
	}
	for index := len(items) - 1; index >= 0; index-- {
		item := items[index]
		if item.Role != "" && item.Role != "user" {
			continue
		}
		if text := textFromContent(item.Content); strings.TrimSpace(text) != "" {
			return text
		}
	}
	return ""
}

func textFromContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var plain string
	if errPlain := json.Unmarshal(raw, &plain); errPlain == nil {
		return plain
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if errBlocks := json.Unmarshal(raw, &blocks); errBlocks != nil {
		return ""
	}
	var builder strings.Builder
	for _, block := range blocks {
		// "text" is the Messages shape; "input_text" is the Responses shape.
		if block.Type != "text" && block.Type != "input_text" {
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(block.Text)
	}
	return builder.String()
}
