package client

type PreJobExecutionHooksFactory func() PreJobExecutionHooks
type PostJobExecutionHooksFactory func() PostJobExecutionHooksFactory

type PreJobExecutionHooks interface {
	Run([]byte) (interface{}, error)
	ValidateParameters([]byte) (interface{}, error)
}

type PostJobExecutionHooks interface {
	Run([]byte) (interface{}, error)
	ValidateParameters([]byte) (interface{}, error)
}
