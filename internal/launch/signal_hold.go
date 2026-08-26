// Package launch runs a delegated round on a provider CLI and leaves the
// evidence a watcher needs behind it.
package launch

import (
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// A launcher's job after the child starts is paperwork: wait, then write the
// sentinel and close the registry entry. That paperwork is the only record a
// watcher has, so a signal to the WRAPPER must never destroy it.
//
// The field incident, twice in one week (2026-08-17): a caller's timeout
// SIGTERMed the foreground wrapper, the child round survived and finished
// correctly, and the sentinel writer died with the wrapper — every watcher keyed
// on the .rc file waited on a round that had already delivered.
//
// So TERM/INT/HUP mean "hold and finish the paperwork", not "abandon it". The
// child is deliberately NOT forwarded the signal: the observed incident is a
// healthy round outliving a disposable wrapper, and forwarding would turn a
// caller's bookkeeping timeout into a round kill. To abort a round, kill the
// child pid itself (the registry shows it); a terminal Ctrl-C already reaches the
// child through the foreground process group, which is why the child is left in
// this process's group.
//
// SIGKILL cannot be held, and the last-resort sentinel does not run for it
// either. That residue is accepted and documented here.
type signalHold struct{ got atomic.Value }

func holdSignals() *signalHold {
	h := &signalHold{}
	ch := make(chan os.Signal, 4)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	go func() {
		for s := range ch {
			// First one wins, matching the shell: the breadcrumb names what the
			// wrapper survived, and a second signal does not make it truer.
			if h.got.Load() == nil {
				switch s {
				case syscall.SIGTERM:
					h.got.Store("TERM")
				case syscall.SIGINT:
					h.got.Store("INT")
				case syscall.SIGHUP:
					h.got.Store("HUP")
				}
			}
		}
	}()
	return h
}

// name is "" when nothing was survived — and also when there is no hold at
// all, so a caller that only wants to render the answer does not have to own
// one. A run always installs a hold; a test rendering the sentinel does not.
func (h *signalHold) name() string {
	if h == nil {
		return ""
	}
	if v := h.got.Load(); v != nil {
		return v.(string)
	}
	return ""
}
