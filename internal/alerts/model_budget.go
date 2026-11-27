package alerts

// ModelBudget is the live actor's first model token-budget refusal.
const ModelBudget = "model_budget"

func init() { Kinds = append(Kinds, ModelBudget); DefaultEvents = append(DefaultEvents, ModelBudget) }
