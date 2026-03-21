package native

// RegisterFASETools is a placeholder until the native adapter's in-process
// FASE service bridge is split behind an acyclic interface.
func RegisterFASETools(_ *ToolRegistry, _ any) error {
	return nil
}
