package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"

	"dbreader/internal/config"
	"dbreader/internal/dbeaver"
	"dbreader/internal/logging"
	"dbreader/internal/ui"
)

func main() {
	cfgFlag := flag.String("config", "", "path to settings.json (default: ./settings.json or ~/.config/dbreader/settings.json)")
	connFlag := flag.String("connection", "", "open this connection directly")
	dbeaverFlag := flag.String("dbeaver", "", `DBeaver data, workspace or project directory ("off" to ignore DBeaver)`)
	logFlag := flag.String("log", logging.DefaultPath(), `log file path ("off" to disable)`)
	debugFlag := flag.Bool("debug", false, "verbose logging")
	initFlag := flag.Bool("init", false, "write the DBeaver connections to settings.json (-config, else the one in use, else ./settings.json) to set their readOnly/protect flags, then exit")
	flag.Parse()

	logFile, err := logging.Setup(*logFlag, *debugFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		os.Exit(1)
	}
	defer logFile.Close()

	if err := run(*cfgFlag, *connFlag, *dbeaverFlag, *logFlag, *initFlag); err != nil {
		slog.Error("fatal", "err", err)
		fmt.Fprintln(os.Stderr, err)
		logFile.Close()
		os.Exit(1)
	}
}

func run(cfgFlag, connFlag, dbeaverFlag, logPath string, initFlag bool) error {
	settings := &config.Settings{}
	path, pathErr := config.Path(cfgFlag)
	switch {
	case pathErr == nil:
		var err error
		settings, err = config.Load(path)
		switch {
		case initFlag && errors.Is(err, fs.ErrNotExist):
			settings = &config.Settings{} // -init creates it
		case err != nil:
			return err
		}
	case cfgFlag != "":
		return pathErr
	}
	if dbeaverFlag != "" {
		settings.DBeaver = dbeaverFlag
	}
	ws, err := findDBeaver(settings)
	if err != nil {
		return err
	}
	if initFlag {
		return initSettings(cfgFlag, path, ws)
	}
	if err := link(settings, ws); err != nil {
		return err
	}
	if len(settings.Profiles) == 0 {
		if pathErr != nil {
			return fmt.Errorf("%w, and no DBeaver workspace either", pathErr)
		}
		return fmt.Errorf("%s has no profiles, and no DBeaver workspace was found", path)
	}
	slog.Info("start", "config", path, "profiles", len(settings.Profiles))

	// connections from settings.json keep their scripts in dbreader's own directory
	if data := config.DataDir(); data != "" {
		for i := range settings.Profiles {
			if p := &settings.Profiles[i]; p.ScriptDir == "" {
				p.UseScriptDir(filepath.Join(data, "scripts", config.SafeName(p.Name)))
			}
		}
	}

	if connFlag != "" {
		found := false
		for _, p := range settings.Profiles {
			if p.Name == connFlag {
				settings.Profiles = []config.Profile{p}
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("connection %q not found", connFlag)
		}
	}

	err = ui.Run(settings, logPath)
	slog.Info("exit")
	return err
}

func findDBeaver(s *config.Settings) (*dbeaver.Workspace, error) {
	if s.DBeaver == "off" {
		return nil, nil
	}
	ws, err := dbeaver.Find(s.DBeaver)
	if err != nil {
		return nil, fmt.Errorf("dbeaver: %w", err)
	}
	if ws != nil {
		slog.Info("dbeaver workspace", "dir", ws.Dir, "connections", len(ws.Profiles), "unsupported", ws.Skipped)
	}
	return ws, nil
}

// initSettings writes the DBeaver connections into settings.json, where
// their readOnly and protect flags can be edited. Logins are not copied.
func initSettings(cfgFlag, found string, ws *dbeaver.Workspace) error {
	if ws == nil {
		return fmt.Errorf("no DBeaver workspace found (looked in %v); pass -dbeaver <dir>", dbeaver.DefaultDirs())
	}
	path := "settings.json"
	switch {
	case cfgFlag != "":
		path = cfgFlag
	case found != "":
		path = found // extend the file that is in use
	}
	added, err := config.Init(path, ws.Profiles)
	if err != nil {
		return err
	}
	abs, _ := filepath.Abs(path)
	if len(added) == 0 {
		fmt.Printf("%s already lists every DBeaver connection\n", abs)
		return nil
	}
	fmt.Printf("%s: added %d connection(s) from %s\n", abs, len(added), ws.Dir)
	for _, name := range added {
		fmt.Printf("  %s\n", name)
	}
	if len(ws.Skipped) > 0 {
		fmt.Printf("not supported, left out: %v\n", ws.Skipped)
	}
	fmt.Println(`
Each one starts with "readOnly": true. To allow writes on a connection set
"readOnly": false; add "protect": true to require typing its name for every
write. Hosts, logins and scripts stay in DBeaver and are read from there.`)
	return nil
}

// link merges the DBeaver connections into the settings. An entry in
// settings.json with the name of a DBeaver connection overlays it (flags,
// and target or login where given); the other DBeaver connections follow,
// read-only if readOnlyDefault is set.
func link(s *config.Settings, ws *dbeaver.Workspace) error {
	linked := map[string]config.Profile{}
	if ws != nil {
		for _, p := range ws.Profiles {
			linked[p.Name] = p
		}
		s.Scripts = append(s.Scripts, ws.Scripts...)
	}
	for i, own := range s.Profiles {
		p, ok := linked[own.Name]
		switch {
		case ok:
			s.Profiles[i] = config.Overlay(own, p)
			delete(linked, own.Name)
		case own.Driver == "":
			return fmt.Errorf("profile %q has no driver and DBeaver has no connection of that name", own.Name)
		}
	}
	if ws != nil {
		for _, p := range ws.Profiles {
			if _, rest := linked[p.Name]; rest {
				p.ReadOnly = p.ReadOnly || s.ReadOnlyDefault
				s.Profiles = append(s.Profiles, p)
				delete(linked, p.Name) // DBeaver allows duplicate names; the first wins
			}
		}
	}
	return nil
}
