package validate

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/BeomSeogKim/Partitur/internal/cast"
)

type acquisitionDependencies struct {
	workingDirectory func() (string, error)
	userHome         func() (string, error)
	readFile         func(string) ([]byte, error)
}

type inputs struct {
	root   string
	score  []byte
	layers []cast.Layer
}

func acquire(dependencies acquisitionDependencies) (inputs, *Refusal) {
	root, err := dependencies.workingDirectory()
	if err != nil {
		return inputs{}, &Refusal{
			Kind:   RefusalWorkingDirectory,
			Detail: err.Error(),
		}
	}

	scorePath := filepath.Join(root, "partitur.yaml")
	scoreData, err := dependencies.readFile(scorePath)
	if err != nil {
		return inputs{}, &Refusal{
			Kind:   RefusalRequiredInput,
			Path:   scorePath,
			Detail: err.Error(),
		}
	}

	result := inputs{
		root:  root,
		score: scoreData,
	}
	projectPath := filepath.Join(root, ".partitur", "cast.yaml")
	projectData, present, refusal := readOptional(
		dependencies.readFile,
		projectPath,
	)
	if refusal != nil {
		return inputs{}, refusal
	}
	if present {
		result.layers = append(result.layers, cast.Layer{
			Origin: "project",
			Data:   projectData,
		})
	}

	home, err := dependencies.userHome()
	if err != nil {
		return inputs{}, &Refusal{
			Kind:   RefusalUserHomeDirectory,
			Detail: err.Error(),
		}
	}
	userPath := filepath.Join(home, ".config", "partitur", "cast.yaml")
	userData, present, refusal := readOptional(
		dependencies.readFile,
		userPath,
	)
	if refusal != nil {
		return inputs{}, refusal
	}
	if present {
		result.layers = append(result.layers, cast.Layer{
			Origin: "user-global",
			Data:   userData,
		})
	}
	return result, nil
}

func readOptional(
	readFile func(string) ([]byte, error),
	path string,
) ([]byte, bool, *Refusal) {
	data, err := readFile(path)
	if err == nil {
		return data, true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	return nil, false, &Refusal{
		Kind:   RefusalDiscoveredInput,
		Path:   path,
		Detail: err.Error(),
	}
}
