package retry

import (
	"bufio"
	"bytes"
	"io/ioutil"
	"os"
	"path/filepath"
	"text/template"
)

const (
	cluster = "mycluster"
)

func parseEnvoyTemplate(input string) *template.Template {
	tmpl := template.New("envoy")
	_, err := tmpl.Parse(input)
	if err != nil {
		panic("unable to parse Envoy config: %s" + err.Error())
	}
	return tmpl
}

func createYamlFile(tempDir, prefix string, tpl *template.Template, data interface{}) (string, error) {
	yamlFile, err := createTempfile(tempDir, prefix, ".yaml")
	if err != nil {
		return "", err
	}

	var filled bytes.Buffer
	w := bufio.NewWriter(&filled)
	if err := tpl.Execute(w, data); err != nil {
		return "", err
	}
	if err := w.Flush(); err != nil {
		return "", err
	}
	w.Flush()

	// Write the content of the file.
	configBytes := filled.Bytes()
	if err := ioutil.WriteFile(yamlFile, configBytes, 0644); err != nil {
		return "", err
	}
	return yamlFile, nil
}

func createTempfile(tmpDir, prefix, suffix string) (string, error) {
	f, err := ioutil.TempFile(tmpDir, prefix)
	if err != nil {
		return "", err
	}
	var tmpName string
	if tmpName, err = filepath.Abs(f.Name()); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Remove(tmpName); err != nil {
		return "", err
	}
	return tmpName + suffix, nil
}
