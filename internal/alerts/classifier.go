package alerts

// JudgeUnavailable is one trusted optional-judge outage in the live actor.
const JudgeUnavailable = "judge_unavailable"

func init() {
	Kinds = append(Kinds, JudgeUnavailable)
	DefaultEvents = append(DefaultEvents, JudgeUnavailable)
}
