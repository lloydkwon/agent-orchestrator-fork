package lifecycle

// LADO fork: the reaper waits for the startup restore. After a reboot the
// runtimes of the running sessions are gone, and with the developer's setting
// on the session manager's startup health pass starts their agents again
// (session_manager/lado_startup_restore.go). The reaper ticks every five
// seconds from the moment the lifecycle starts: on a slow start it would see
// those dead runtimes first and mark the sessions terminated, and a
// terminated session is not started again. While the hold says so, a runtime
// death the reaper observes terminates nothing; the next observation after
// the hold lifts decides as before.

// SetLadoRuntimeDeathHold installs the hold. hold reports whether the
// startup restore may yet start agents again; nil holds nothing, which is
// upstream behavior. The daemon installs it once the session manager exists,
// while the reaper already runs, so the field is atomic.
func (m *Manager) SetLadoRuntimeDeathHold(hold func() bool) {
	if hold == nil {
		m.ladoRuntimeDeathHold.Store(nil)
		return
	}
	m.ladoRuntimeDeathHold.Store(&hold)
}

// ladoHoldsRuntimeDeath reports whether an observed runtime death waits.
func (m *Manager) ladoHoldsRuntimeDeath() bool {
	hold := m.ladoRuntimeDeathHold.Load()
	return hold != nil && (*hold)()
}
