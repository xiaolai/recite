package usage

// Tokens is the token usage of one render (realtime.TokenUsage, kept separate so this package
// does not depend on the realtime client).
type Tokens struct {
	Input, Output, InputText, OutputText, OutputAudio int
	Detailed                                          bool // output details were reported
}

// Note labels every estimate: recite cannot see what, if anything, is billed.
const Note = "list price, not billed"

// ModelPrice is the list price of one model. Zero means the table has no rate.
type ModelPrice struct {
	AudioOutPerMTokens float64 // USD per 1M output audio tokens (speech models)
	PerMinute          float64 // USD per minute of transcribed audio (STT models)
}

// PriceTable is a dated list-price table.
type PriceTable struct {
	Date   string // the day the rates were recorded
	Source string
	Models map[string]ModelPrice
}

// ListPrices are the rates recorded in the design on 2026-10-11: audio out per 1M tokens from
// D3, gpt-transcribe per minute from §2. Text tokens and gpt-live-transcribe have no recorded
// rate, so they are not priced; an entry for a model without a rate carries no estimate.
var ListPrices = PriceTable{
	Date:   "2026-10-11",
	Source: "OpenAI list prices as recorded in design D3 and §2",
	Models: map[string]ModelPrice{
		"gpt-realtime-2.1":      {AudioOutPerMTokens: 64},
		"gpt-realtime-2.1-mini": {AudioOutPerMTokens: 20},
		"gpt-realtime-2":        {AudioOutPerMTokens: 64},
		"gpt-transcribe":        {PerMinute: 0.0045},
	},
}

// Label is the note every estimate carries: what it is and where the rates come from.
func (p PriceTable) Label() string { return Note + "; " + p.Source + ", " + p.Date }

// Render estimates one render. Only output audio tokens are priced; when the usage carries no
// output details, every output token is counted as audio, which can only over-estimate.
func (p PriceTable) Render(model string, u Tokens) (float64, bool) {
	mp, ok := p.Models[model]
	if !ok || mp.AudioOutPerMTokens == 0 {
		return 0, false
	}
	audio := u.OutputAudio
	if !u.Detailed {
		audio = u.Output
	}
	return float64(audio) * mp.AudioOutPerMTokens / 1e6, true
}

// STT estimates one transcription of the given billed seconds.
func (p PriceTable) STT(model string, seconds int) (float64, bool) {
	mp, ok := p.Models[model]
	if !ok || mp.PerMinute == 0 {
		return 0, false
	}
	return float64(seconds) / 60 * mp.PerMinute, true
}

// RenderEntry is the ledger line of one render, priced from p.
func (p PriceTable) RenderEntry(e Entry, u Tokens) Entry {
	e.Kind = KindRender
	e.InputTokens, e.OutputTokens = u.Input, u.Output
	e.InputTextTokens, e.OutputTextTokens, e.OutputAudioTokens = u.InputText, u.OutputText, u.OutputAudio
	e.EstimateUSD, e.PriceNote = nil, p.Label()
	if usd, ok := p.Render(e.Model, u); ok {
		e.EstimateUSD = &usd
	}
	return e
}

// STTEntry is the ledger line of one STT call, priced from p.
func (p PriceTable) STTEntry(e Entry, seconds int) Entry {
	e.Kind = KindSTT
	e.STTSeconds = seconds
	e.EstimateUSD, e.PriceNote = nil, p.Label()
	if usd, ok := p.STT(e.Model, seconds); ok {
		e.EstimateUSD = &usd
	}
	return e
}
