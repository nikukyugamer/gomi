package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	clilog "github.com/b4b4r07/go-cli-log"
	"github.com/dustin/go-humanize"
	"github.com/gabriel-vasile/mimetype"
	"github.com/jessevdk/go-flags"
	"github.com/manifoldco/promptui"
	"github.com/rs/xid"
	"golang.org/x/crypto/ssh/terminal"
	"golang.org/x/sync/errgroup"
)

const gomiDir = ".gomi"

var (
	Version  = "unset"
	Revision = "unset"
)

var (
	gomiPath      = filepath.Join(os.Getenv("HOME"), gomiDir)
	inventoryFile = "inventory.json"
	inventoryPath = filepath.Join(gomiPath, inventoryFile)
)

type Option struct {
	Restore  bool     `short:"b" long:"restore" description:"Restore deleted file"`
	Version  bool     `long:"version" description:"Show version"`
	RmOption RmOption `group:"Dummy options"`
}

type RmOption struct {
	Interactive bool `short:"i" description:"To make compatible with rm command"`
	Recursive   bool `short:"r" description:"To make compatible with rm command"`
	Force       bool `short:"f" description:"To make compatible with rm command"`
	Directory   bool `short:"d" description:"To make compatible with rm command"`
	Verbose     bool `short:"v" description:"To make compatible with rm command"`
}

type Inventory struct {
	Path  string `json:"path"`
	Files []File `json:"files"`
}

type File struct {
	Name      string    `json:"name"`     // file.go
	ID        string    `json:"id"`       // asfasfafd
	GroupID   string    `json:"group_id"` // zoapompji
	From      string    `json:"from"`     // $PWD/file.go
	To        string    `json:"to"`       // ~/.gomi/2020/01/16/zoapompji/file.go.asfasfafd
	Timestamp time.Time `json:"timestamp"`
}

type CLI struct {
	Option    Option
	Inventory Inventory
}

func (i *Inventory) Open() error {
	log.Printf("[DEBUG] opening inventry")
	f, err := os.Open(i.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewDecoder(f).Decode(&i)
}

func (i *Inventory) Update(files []File) error {
	log.Printf("[DEBUG] updating inventry")
	f, err := os.Create(i.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	i.Files = files
	return json.NewEncoder(f).Encode(&i)
}

func (i *Inventory) Save(files []File) error {
	log.Printf("[DEBUG] saving inventry")
	f, err := os.Create(i.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	i.Files = append(i.Files, files...)
	return json.NewEncoder(f).Encode(&i)
}

func (i *Inventory) Delete(target File) error {
	log.Printf("[DEBUG] deleting %v from inventry", target)
	var files []File
	for _, file := range i.Files {
		if file.ID == target.ID {
			continue
		}
		files = append(files, file)
	}
	return i.Update(files)
}

func makeFile(groupID string, arg string) (File, error) {
	id := xid.New().String()
	name := filepath.Base(arg)
	from, err := filepath.Abs(arg)
	if err != nil {
		return File{}, err
	}
	now := time.Now()
	return File{
		Name:    name,
		ID:      id,
		GroupID: groupID,
		From:    from,
		To: filepath.Join(
			gomiPath,
			fmt.Sprintf("%04d", now.Year()),
			fmt.Sprintf("%02d", now.Month()),
			fmt.Sprintf("%02d", now.Day()),
			groupID, fmt.Sprintf("%s.%s", name, id),
		),
		Timestamp: now,
	}, nil
}

func (f File) ToJSON(w io.Writer) {
	out, err := json.Marshal(&f)
	if err != nil {
		return
	}
	fmt.Fprint(w, string(out))
}

func isBinary(path string) bool {
	detectedMIME, err := mimetype.DetectFile(path)
	if err != nil {
		return true
	}
	isBinary := true
	for mime := detectedMIME; mime != nil; mime = mime.Parent() {
		if mime.Is("text/plain") {
			isBinary = false
		}
	}
	return isBinary
}

func head(path string) string {
	max := 5
	wrap := func(line string) string {
		line = strings.ReplaceAll(line, "\t", "  ")
		id := int(os.Stdout.Fd())
		width, _, _ := terminal.GetSize(id)
		if width < 10 {
			return line
		}
		if len(line) < width-10 {
			return line
		}
		return line[:width-10] + "..."
	}
	fi, err := os.Stat(path)
	if err != nil {
		return "(panic: not found)"
	}
	content := func(lines []string) string {
		if len(lines) == 0 {
			return "(no content)"
		}
		var content string
		var i int
		for _, line := range lines {
			i++
			content += fmt.Sprintf("  %s\n", wrap(line))
			if i > max {
				content += "  ...\n"
				break
			}
		}
		return content
	}
	var lines []string
	switch {
	case fi.IsDir():
		lines = []string{"(directory)"}
		dirs, _ := ioutil.ReadDir(path)
		for _, dir := range dirs {
			lines = append(lines, fmt.Sprintf("%s\t%s", dir.Mode().String(), dir.Name()))
		}
	default:
		if isBinary(path) {
			return "(binary file)"
		}
		lines = []string{""}
		fp, _ := os.Open(path)
		defer fp.Close()
		s := bufio.NewScanner(fp)
		for s.Scan() {
			lines = append(lines, s.Text())
		}
	}
	return content(lines)
}

func (c CLI) Prompt() (File, error) {
	files := c.Inventory.Files
	if len(files) == 0 {
		return File{}, errors.New("no deleted files found")
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Timestamp.After(files[j].Timestamp)
	})

	funcMap := promptui.FuncMap
	funcMap["time"] = humanize.Time
	funcMap["head"] = head
	templates := &promptui.SelectTemplates{
		Label:    "{{ . }}",
		Active:   promptui.IconSelect + " {{ .Name | cyan }}",
		Inactive: "  {{ .Name | faint }}",
		Selected: promptui.IconGood + " {{ .Name }}",
		Details: `
{{ "Name:" | faint }}	{{ .Name }}
{{ "Path:" | faint }}	{{ .From }}
{{ "DeletedAt:" | faint }}	{{ .Timestamp | time }}
{{ "Content:" | faint }}	{{ .To | head }}
		`,
		FuncMap: funcMap,
	}

	searcher := func(input string, index int) bool {
		file := files[index]
		name := strings.Replace(strings.ToLower(file.Name), " ", "", -1)
		input = strings.Replace(strings.ToLower(input), " ", "", -1)
		return strings.Contains(name, input)
	}

	prompt := promptui.Select{
		Label:             "Which to restore?",
		Items:             files,
		Templates:         templates,
		Searcher:          searcher,
		StartInSearchMode: true,
		HideSelected:      true,
	}

	i, _, err := prompt.Run()
	return files[i], err
}

func (c CLI) Restore() error {
	file, err := c.Prompt()
	if err != nil {
		return err
	}
	defer c.Inventory.Delete(file)
	_, err = os.Stat(file.From)
	if err == nil {
		// already exists so to prevent to overwrite
		// add id to the end of filename
		file.From = file.From + "." + file.ID
	}
	log.Printf("[DEBUG] restoring %q -> %q", file.To, file.From)
	return os.Rename(file.To, file.From)
}

func (c CLI) Remove(args []string) error {
	if len(args) == 0 {
		return errors.New("too few aruments")
	}

	files := make([]File, len(args))
	groupID := xid.New().String()

	var eg errgroup.Group

	for i, arg := range args {
		i, arg := i, arg // https://golang.org/doc/faq#closures_and_goroutines
		eg.Go(func() error {
			_, err := os.Stat(arg)
			if os.IsNotExist(err) {
				return fmt.Errorf("%s: no such file or directory", arg)
			}
			file, err := makeFile(groupID, arg)
			if err != nil {
				return err
			}

			// For debugging
			var buf bytes.Buffer
			file.ToJSON(&buf)
			log.Printf("[DEBUG] generating file metadata: %s", buf.String())

			files[i] = file
			os.MkdirAll(filepath.Dir(file.To), 0777)
			log.Printf("[DEBUG] moving %q -> %q", file.From, file.To)
			return os.Rename(file.From, file.To)
		})
	}
	defer c.Inventory.Save(files)

	if c.Option.RmOption.Force {
		return nil
	}
	return eg.Wait()
}

func (c CLI) Run(args []string) error {
	c.Inventory.Open()

	switch {
	case c.Option.Version:
		fmt.Fprintf(os.Stdout, "%s (%s)\n", Version, Revision)
		return nil
	case c.Option.Restore:
		return c.Restore()
	default:
	}

	return c.Remove(args)
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	clilog.Env = "GOMI_LOG"
	clilog.SetOutput()
	defer log.Printf("[INFO] finish main function")

	log.Printf("[INFO] Version: %s (%s)", Version, Revision)
	log.Printf("[INFO] gomiPath: %s", gomiPath)
	log.Printf("[INFO] inventoryPath: %s", inventoryPath)

	var option Option

	// if making error output, ignore PrintErrors from Default
	// flags.Default&^flags.PrintErrors
	// https://godoc.org/github.com/jessevdk/go-flags#pkg-constants
	parser := flags.NewParser(&option, flags.HelpFlag|flags.PrintErrors|flags.PassDoubleDash)
	args, err := parser.Parse()
	if err != nil {
		log.Printf("[ERROR] failed to run parser: %v", err)
		return 2
	}

	cli := CLI{
		Option:    option,
		Inventory: Inventory{Path: inventoryPath},
	}

	log.Printf("[INFO] Args: %v", args)
	if err := cli.Run(args); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 1
	}

	return 0
}