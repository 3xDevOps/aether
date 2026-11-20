package vm

import (
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/3xDevOps/Aether/internal/runtime"
	"github.com/3xDevOps/Aether/internal/vmimage"
)

// programDisk is the disk of an agent program a VM booted with, and where
// the guest binds the program.
type programDisk struct {
	Disk   vmimage.Disk `json:"disk"`
	Target string       `json:"target"`
}

func programSerial(i int) string { return "program-" + strconv.Itoa(i) }

// attachPrograms gives a VM about to boot cold the disk of each of its
// programs as the home holds it now. A program with no disk yet loads from
// the home, and its disk is built in the background for the next boot. A
// sleeping VM keeps the disks it booted with.
func (e *engine) attachPrograms(rec *record) error {
	if err := e.releasePrograms(rec); err != nil {
		return err
	}
	for _, p := range rec.Programs {
		unit := e.programUnit(p)
		if unit == "" {
			continue
		}
		link := e.path(rec.Key, programSerial(len(rec.ProgramDisks))+".img")
		d, ok, err := e.cfg.Images.ProgramDisk(p.Home, p.Executable, unit, link)
		switch {
		case err != nil:
			slog.Warn("vm runtime: attach an agent program's disk; the home serves it", "vm", rec.Key, "program", unit, "error", err)
		case ok:
			rec.ProgramDisks = append(rec.ProgramDisks, programDisk{Disk: d, Target: path.Join(p.Target, unit)})
		default:
			e.buildProgram(p, unit)
		}
	}
	return nil
}

// releasePrograms lets go of the program disks of a VM that is down, so the
// sweep can delete a version no VM uses.
func (e *engine) releasePrograms(rec *record) error {
	links, err := filepath.Glob(e.path(rec.Key, "program-*.img"))
	if err != nil {
		return err
	}
	for _, link := range links {
		if err = os.Remove(link); err != nil {
			return fmt.Errorf("vm runtime: let go of an agent program's disk: %w", err)
		}
	}
	rec.ProgramDisks = nil
	return nil
}

// BuildPrograms builds, in the background, the disk of each program as its
// home holds it now, for the next VM that boots with it.
func (r *Runtime) BuildPrograms(programs []runtime.Program) {
	for _, p := range programs {
		if unit := r.vms.programUnit(p); unit != "" {
			r.vms.buildProgram(p, unit)
		}
	}
}

func (e *engine) buildProgram(p runtime.Program, unit string) {
	e.builds.Go(func() {
		if err := e.cfg.Images.BuildProgram(p.Home, p.Executable, unit); err != nil {
			slog.Warn("vm runtime: build an agent program's disk", "program", unit, "error", err)
		}
	})
}

// programUnit is the file or directory p runs from, "" for none the home
// holds or one that cannot be found.
func (e *engine) programUnit(p runtime.Program) string {
	if e.cfg.ProgramUnit == nil {
		return ""
	}
	unit, err := e.cfg.ProgramUnit(p.Home, p.Executable)
	if err != nil {
		slog.Warn("vm runtime: find an agent program; the home serves it", "program", p.Executable, "error", err)
	}
	return unit
}
