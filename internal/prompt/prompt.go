// Package prompt assembles what is sent to the model for one answer.
package prompt

import (
	"fmt"
	"strings"
	"time"
)

// Exchange is an earlier answer given in this session.
type Exchange struct {
	Time     time.Time
	Question string // empty when the user just pressed Help
	Answer   string
}

// Input is everything known at the moment the user asks for help.
type Input struct {
	AnswerPrompt  string     // the user's instruction for how to answer
	Documents     string     // rendered context files, may be empty
	Transcript    string     // recent transcript, may be empty
	Listening     bool       // whether audio is being transcribed at all
	History       []Exchange // earlier answers, oldest first
	Question      string     // typed question; empty for the Help button
	HasScreenshot bool
	Now           time.Time
}

const system = `You are a real-time meeting copilot running privately on the user's computer during a live meeting, call or interview. Only the user sees what you write. Your job is to help the user respond well, right now.

What you are given:
- A live transcript of the meeting. "Me" is the user (their microphone). "Others" is everyone else (the meeting audio). It comes from automatic speech recognition, so expect misheard words, missing punctuation and names spelled wrong; work out what was meant. The microphone can also pick up the others' voices from the speakers, so a "Me" line may repeat an "Others" line.
- A screenshot of the user's screen at this moment, when available.
- Reference documents supplied by the user (for example a resume, notes, or product material), when available.
- Answers you gave earlier in this meeting, when available.

When the user asks for help without a specific question, work out what they need at this moment, in this order:
1. If someone asked the user a question or asked them to do something and it is still unanswered, give the user the answer to say.
2. Otherwise, if the screen shows something to solve or respond to (a coding problem, a question, a document under discussion, an error), help with that.
3. Otherwise, give the most useful next thing to say or ask given where the conversation is.
When the user types a question, answer that question using the same context.

Rules:
- Start with the answer itself. No preamble, no restating the question, no describing the transcript or the screenshot back to the user.
- The user is reading this while people wait for them. Keep it short enough to take in at a glance unless the task needs more (for example, code).
- Use the reference documents for facts about the user, their work or their product. Do not invent personal facts, numbers, names or commitments. If something needed is not in the context, say so in a few words and give the best general answer.
- If the transcript is too garbled to tell what was asked, say what you think was asked in one short line, then answer that.
- Use light Markdown: short paragraphs, a few bullets, fenced code blocks for code.`

// Build returns the system prompt and the user message.
func Build(in Input) (systemPrompt, user string) {
	var sys strings.Builder
	sys.WriteString(system)
	if style := strings.TrimSpace(in.AnswerPrompt); style != "" {
		sys.WriteString("\n\nThe user's instructions for how answers should be given. Follow them; where they conflict with the rules above, they win:\n")
		sys.WriteString(style)
	}

	var b strings.Builder
	if docs := strings.TrimSpace(in.Documents); docs != "" {
		b.WriteString("<documents>\n")
		b.WriteString(docs)
		b.WriteString("\n</documents>\n\n")
	}

	if len(in.History) > 0 {
		b.WriteString("<earlier_answers>\n")
		for _, ex := range in.History {
			asked := "(pressed Help)"
			if q := strings.TrimSpace(ex.Question); q != "" {
				asked = q
			}
			fmt.Fprintf(&b, "[%s] I asked: %s\nYou answered: %s\n\n", ex.Time.Format("15:04:05"), asked, strings.TrimSpace(ex.Answer))
		}
		b.WriteString("</earlier_answers>\n\n")
	}

	b.WriteString("<transcript>\n")
	switch {
	case strings.TrimSpace(in.Transcript) != "":
		b.WriteString(strings.TrimSpace(in.Transcript))
	case in.Listening:
		b.WriteString("(nothing has been said yet)")
	default:
		b.WriteString("(audio is not being transcribed)")
	}
	b.WriteString("\n</transcript>\n\n")

	fmt.Fprintf(&b, "The time is now %s; the last transcript lines are the most recent.\n", in.Now.Format("15:04:05"))
	if in.HasScreenshot {
		b.WriteString("The attached image is my screen right now.\n")
	} else {
		b.WriteString("No screenshot is available for this request.\n")
	}

	b.WriteString("\n")
	if q := strings.TrimSpace(in.Question); q != "" {
		b.WriteString("My question: ")
		b.WriteString(q)
	} else {
		b.WriteString("Help me right now.")
	}
	return sys.String(), b.String()
}

// Truncate shortens s to at most max runes, marking the cut.
func Truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}
