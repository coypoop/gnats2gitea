package main

import (
	"bufio"
	"code.gitea.io/sdk/gitea"
	"encoding/base64"
	"flag"
	"fmt"
	"github.com/DusanKasan/parsemail"
	"github.com/grokify/html-strip-tags-go"
	"html"
	"io/ioutil"
	"mime"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/transform"
)

type Reply struct {
	Email             parsemail.Email
	AttachmentsBase64 []string
	Plaintext         string
	IsEmail           bool
	From              []*mail.Address
	Date              time.Time
}

type GnatsIssue struct {
	Synopsis    string
	Number      string
	State       string
	Category    string
	Environment string
	Replies     []Reply
	Description string
	HowToRepeat string
	Fix         string
}

type ConversionState struct {
	Labels     []*gitea.Label
	Client     *gitea.Client
	GiteaOwner string
	GiteaRepo  string
}

func main() {
	giteaServer := flag.String("gitea-server", "http://localhost:3000", "What's the parent URL of the gitea instance")
	giteaOwner := flag.String("gitea-owner", "gnat-conversion-bot", "Under which user is the repository")
	giteaRepo := flag.String("gitea-repo", "gitea-conversion", "What's the name of the repo")
	giteaToken := os.Getenv("GITEA_TOKEN")
	flag.Parse()

	if giteaOwner == nil || giteaRepo == nil || giteaServer == nil || giteaToken == "" {
		usage()
	}

	client, err := gitea.NewClient(*giteaServer, gitea.SetToken(giteaToken))
	if err != nil {
		panic(err)
	}
	s := ConversionState{
		Client:     client,
		GiteaOwner: *giteaOwner,
		GiteaRepo:  *giteaRepo,
	}
	_, _, err = client.GetMyUserInfo()
	if err != nil {
		panic(err)
	}
	s.init_labels()
	//s.ensure_issue(29)
	//s.ensure_issue(171)
	i := 1
	for i < 56000 {
		fmt.Printf("Handling issue %d\n", i)
		s.ensure_issue(i)
		i++
	}
	//s.create_issue(49089)

}

func (s *ConversionState) ensure_issue_exists(new_content GiteaIssue) {
	_, _, err := s.Client.GetIssue(s.GiteaOwner, s.GiteaRepo, new_content.ID)
	if err != nil {
		createIssueOpts := gitea.CreateIssueOption{
			Title:  new_content.Title,
			Body:   new_content.Body,
			Labels: new_content.Labels,
			Closed: new_content.Closed,
		}
		_, _, err := s.Client.CreateIssue(s.GiteaOwner, s.GiteaRepo, createIssueOpts)
		if err != nil {
			panic(err)
		}
	}
}

func (s *ConversionState) ensure_comments(new_content GiteaIssue) {
	existing_comments := s.get_issue_comments(new_content.ID)
	for i, reply := range new_content.Replies {
		if i+1 > len(existing_comments) {
			fmt.Printf("On issue %d, comment index %d doesn't exist, creating\n", new_content.ID, i)
			createIssueCommentOpts := gitea.CreateIssueCommentOption{
				Body: reply,
			}
			if _, _, err := s.Client.CreateIssueComment(s.GiteaOwner, s.GiteaRepo, new_content.ID, createIssueCommentOpts); err != nil {
				panic(err)
			}
		} else {
			fmt.Println("Comment already exists, doing nothing")
		}
	}
}

func (s *ConversionState) ensure_issue(id int) {
	gnats_issue := parse_gnats(id)
	new_content := convert_gitea(gnats_issue, s.Labels)

	s.ensure_issue_exists(new_content)
	s.ensure_comments(new_content)
}

func (s *ConversionState) init_labels() {
	if len(s.Labels) == 0 {
		// Get them from the server
		s.Labels = s.get_labels()
	}
	if len(s.Labels) == 0 {
		// Not in the server either, create
		s.Labels = s.create_labels()
	}
}

func (s *ConversionState) create_issue(id int) {
	gnats_issue := parse_gnats(id)
	if gnats_issue == nil {
		return
	}
	new_content := convert_gitea(gnats_issue, s.Labels)

	createIssueOpts := gitea.CreateIssueOption{
		Title:  new_content.Title,
		Body:   new_content.Body,
		Labels: new_content.Labels,
		Closed: new_content.Closed,
	}
	createdIssue, _, err := s.Client.CreateIssue(s.GiteaOwner, s.GiteaRepo, createIssueOpts)
	if err != nil {
		panic(err)
	}

	for _, reply := range new_content.Replies {
		createIssueCommentOpts := gitea.CreateIssueCommentOption{
			Body: reply,
		}
		if _, _, err := s.Client.CreateIssueComment(s.GiteaOwner, s.GiteaRepo, createdIssue.ID, createIssueCommentOpts); err != nil {
			panic(err)
		}
	}
}

func (s *ConversionState) get_issue_comments(id int64) []*gitea.Comment {
	comments, _, err := s.Client.ListIssueComments(s.GiteaOwner, s.GiteaRepo, id, gitea.ListIssueCommentOptions{
		Before: time.Now(),
	})
	if err != nil {
		return []*gitea.Comment{}
	}
	return comments
}

func (s *ConversionState) get_labels() []*gitea.Label {
	labels := []*gitea.Label{}
	i := 0
	for {
		labelPage, _, err := s.Client.ListRepoLabels(s.GiteaOwner, s.GiteaRepo, gitea.ListLabelsOptions{gitea.ListOptions{Page: i}})
		if err != nil {
			panic(err)
		}
		if len(labelPage) == 0 {
			break
		}
		labels = append(labels, labelPage...)
		i++
	}
	return labels
}

func (s *ConversionState) create_labels() (createdLabels []*gitea.Label) {
	var labelColors = []string{
		"#b60205", "#e99695",
		"#d93f0b", "#f9d0c4",
		"#fbca04", "#fef2c0",
		"#0e8a16", "#c2e0c6",
		"#006b75", "#bfdadc",
		"#1d76db", "#c5def5",
		"#0052cc", "#bfd4f2",
		"#5319e7", "#d4c5f9",
	}
	var stateLabels = map[string]string{
		"analyzed":        "Analyzed",
		"feedback":        "Pending feedback from reporter",
		"suspended":       "Suspended",
		"needs-pullups":   "Should request a backport of this to a release branch",
		"pending-pullups": "Waiting for a backport request to be fulfilled by release engineering",
	}
	var categoryLabels = []string{
		"admin",
		"bin",
		"install",
		"kern",
		"lib",
		"misc",
		"pkg",
		"port-acorn32",
		"port-algor",
		"port-alpha",
		"port-amd64",
		"port-amiga",
		"port-amigappc",
		"port-arc",
		"port-arm",
		"port-arm32",
		"port-atari",
		"port-bebox",
		"port-cats",
		"port-cobalt",
		"port-dreamcast",
		"port-emips",
		"port-evbarm",
		"port-evbmips",
		"port-evbppc",
		"port-evbsh3",
		"port-ews4800mips",
		"port-hp300",
		"port-hppa",
		"port-hpcarm",
		"port-hpcmips",
		"port-hpcsh",
		"port-hppa",
		"port-i386",
		"port-ia64",
		"port-ibmnws",
		"port-iyonix",
		"port-luna68k",
		"port-m68k",
		"port-mac68k",
		"port-macppc",
		"port-mips",
		"port-mipsco",
		"port-mvme68k",
		"port-mvmeppc",
		"port-netwinder",
		"port-news68k",
		"port-newsmips",
		"port-next68k",
		"port-ofppc",
		"port-pc532",
		"port-playstation2",
		"port-pmax",
		"port-pmppc",
		"port-powerpc",
		"port-prep",
		"port-sandpoint",
		"port-sbmips",
		"port-sgimips",
		"port-sh3",
		"port-shark",
		"port-sparc",
		"port-sparc64",
		"port-sun2",
		"port-sun3",
		"port-vax",
		"port-x68k",
		"port-xen",
		"port-zaurus",
		"security",
		"standards",
		"test",
		"lib",
		"toolchain",
		"xsrc",
		"confidential",
	}
	for i, label := range categoryLabels {
		color := labelColors[i%len(labelColors)]
		createLabelOpts := gitea.CreateLabelOption{
			Name:        label,
			Color:       color,
			Description: "Bugs with a category of " + label,
		}
		createdLabel, _, err := s.Client.CreateLabel(s.GiteaOwner, s.GiteaRepo, createLabelOpts)
		fmt.Println("Creating label " + label)
		if err != nil {
			panic(err)
		}
		createdLabels = append(createdLabels, createdLabel)
	}
	i := 0
	for label, description := range stateLabels {
		color := labelColors[i%len(labelColors)]
		createLabelOpts := gitea.CreateLabelOption{
			Name:        label,
			Color:       color,
			Description: description,
		}
		createdLabel, _, err := s.Client.CreateLabel(s.GiteaOwner, s.GiteaRepo, createLabelOpts)
		if err != nil {
			panic(err)
		}
		createdLabels = append(createdLabels, createdLabel)
		i++
	}
	return createdLabels
}

type GiteaIssue struct {
	ID      int64
	Title   string
	Body    string
	Replies []string
	Labels  []int64
	Closed  bool
}

func format_email_reply(reply Reply) string {
	dec := new(mime.WordDecoder)
	text := ""
	for _, from := range reply.From {
		fromDecoded, err := dec.DecodeHeader(from.String())
		if err != nil {
			panic(err)
		}
		text += fmt.Sprintf("From %s on %s\n", fromDecoded, reply.Date.String())
	}
	text += fmt.Sprintf("```\n%s\n```", reply.Plaintext)
	return text
}

func format_status_change(reply Reply) string {
	return reply.Plaintext
}

func get_label_by_name(name string, labels []*gitea.Label) *gitea.Label {
	for _, label := range labels {
		if label.Name == name {
			return label
		}
	}
	panic("Tried to get label by name " + name + ". Not found")
	return nil
}

func get_label_ids(gnatsIssue *GnatsIssue, labels []*gitea.Label) []int64 {
	category_label := get_label_by_name(gnatsIssue.Category, labels)
	label_ids := []int64{category_label.ID}
	switch gnatsIssue.State {
	case "closed":
	case "open":
		break
	default:
		issue_label := get_label_by_name(gnatsIssue.State, labels)
		label_ids = append(label_ids, issue_label.ID)
	}
	return label_ids

}

func fixup_title(title string) string {
	// gitea can't handle empty titles, but GNATS can. (PR #171)
	if title == "" {
		fmt.Println("Empty title, padding")
		return "No title"
	}
	return title
}

func convert_gitea(gnatsIssue *GnatsIssue, labels []*gitea.Label) GiteaIssue {
	body := fmt.Sprintf("```Description:\n%s\nHow-To-Repeat:\n%s\nFix:\n%s```", gnatsIssue.Description, gnatsIssue.HowToRepeat, gnatsIssue.Fix)
	giteaReplies := []string{}
	for _, reply := range gnatsIssue.Replies {
		if reply.IsEmail {
			giteaReplies = append(giteaReplies, format_email_reply(reply))
		} else {
			giteaReplies = append(giteaReplies, format_status_change(reply))
		}
	}
	id, err := strconv.ParseInt(gnatsIssue.Number, 10, 64)
	if err != nil {
		panic(err)
	}
	return GiteaIssue{
		ID:      id,
		Title:   fixup_title(gnatsIssue.Synopsis),
		Body:    body,
		Replies: giteaReplies,
		Labels:  get_label_ids(gnatsIssue, labels),
		Closed:  gnatsIssue.State == "closed",
	}
}

func HelloServer(w http.ResponseWriter, r *http.Request) {
	number, err := strconv.Atoi(r.URL.Path[1:])
	if err != nil {
		fmt.Fprintf(w, "Bad data!")
		return
	}
	output := parse_gnats(number)
	fmt.Fprintf(w, "%s", output)
}

func parse_gnats(number int) *GnatsIssue {
	var replies []Reply
	url := "https://gnats.netbsd.org/" + strconv.Itoa(number)
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()
	decodingReader := transform.NewReader(resp.Body, charmap.Windows1252.NewDecoder())
	raw_html, err := ioutil.ReadAll(decodingReader)
	if err != nil {
		panic(err)
	}
	raw_gnats_bug := html.UnescapeString(strip.StripTags(string(raw_html)))
	if !strings.Contains(raw_gnats_bug, "Audit-Trail") {
		// Confidential bug, we have no access
		return &GnatsIssue{
			Number:      strconv.Itoa(number),
			Synopsis:    "confidential bug report",
			Category:    "confidential",
			State:       "closed",
			Environment: "",
			Description: "Couldn't access bug report",
			HowToRepeat: "",
			Fix:         "",
			Replies:     []Reply{},
		}
	}
	dictionary := split_to_attributes(raw_gnats_bug)
	email_replies_raw := pair_value_by_key(dictionary, "Audit-Trail")
	for _, single_reply_raw := range split_replies(email_replies_raw) {
		var current_email parsemail.Email
		var current_reply Reply
		single_reply_fixed := repair_gnats_damage(single_reply_raw)
		current_email, err := parsemail.Parse(strings.NewReader(single_reply_fixed))
		if err == nil {
			current_reply.Email = current_email
			current_reply.IsEmail = true
			for _, a := range current_email.Attachments {
				attachmentData, err := ioutil.ReadAll(a.Data)
				if err != nil {
					panic(err)
				}
				attachmentBase64 := base64.StdEncoding.EncodeToString(attachmentData)
				current_reply.AttachmentsBase64 = append(current_reply.AttachmentsBase64, attachmentBase64)
			}
			current_reply.Plaintext = current_email.TextBody
			current_reply.From = current_email.From
			current_reply.Date = current_email.Date
		} else {
			// Not valid email (probably state change)
			// Treat as plaintext
			current_reply.IsEmail = false
			current_reply.Plaintext = single_reply_raw
		}
		replies = append(replies, current_reply)
	}

	return &GnatsIssue{
		Synopsis:    pair_value_by_key(dictionary, "Synopsis"),
		Number:      pair_value_by_key(dictionary, "Number"),
		Category:    pair_value_by_key(dictionary, "Category"),
		State:       pair_value_by_key(dictionary, "State"),
		Environment: pair_value_by_key(dictionary, "Environment"),
		Description: pair_value_by_key(dictionary, "Description"),
		HowToRepeat: pair_value_by_key(dictionary, "How-To-Repeat"),
		Fix:         pair_value_by_key(dictionary, "Fix"),
		Replies:     replies,
	}
}

func parse_gnats_time(original_time string) time.Time {
	// Reference time as formatted by GNATS
	const gnatsForm = "Mon Jan 2 15:04:05 -0700 2006"

	t, err := time.Parse(gnatsForm, original_time)
	if err != nil {
		panic(err)
	}

	return t
}

func split_replies(replies_block string) []string {
	scanner := bufio.NewScanner(strings.NewReader(replies_block))
	scanner.Split(bufio.ScanLines)

	var result []string
	var keep_state bool = false
	var current_match0 string
	for scanner.Scan() {
		// About to reach next key:value pair
		// do we have a previous match to save?
		if ((strings.HasPrefix(scanner.Text(), "From:") ||
			strings.HasPrefix(scanner.Text(), "State-Changed-From-To:")) ||
			strings.HasPrefix(scanner.Text(), "Responsible-Changed-From-To:")) &&
			keep_state {
			result = append(result, current_match0)
			keep_state = false
		}

		if (strings.HasPrefix(scanner.Text(), "From:") ||
			strings.HasPrefix(scanner.Text(), "State-Changed-From-To:")) ||
			strings.HasPrefix(scanner.Text(), "Responsible-Changed-From-To:") {
			keep_state = true
			current_match0 = scanner.Text()
		} else if keep_state {
			// multi-line match: keep old results
			current_match0 += "\n"
			// User-controlled content starts with a space
			current_match0 += strings.TrimPrefix(scanner.Text(), " ")
		}
	}
	if keep_state {
		// Append last match
		result = append(result, current_match0)
	}

	return result
}

func repair_gnats_damage(raw_email string) string {
	s0 := repair_gnats_space(raw_email)
	s1 := repair_gnats_boundary(s0)

	return s1
}

// Remove leading space in user-controlled content
func repair_gnats_space(replies_block string) string {
	var result string

	scanner := bufio.NewScanner(strings.NewReader(replies_block))
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		result += strings.TrimPrefix(scanner.Text(), " ")
		result += "\n"
	}

	return result
}

// Header or linear white-space ("LWSP")
func is_email_header(email_line string) bool {
	return strings.Contains(email_line, ":") ||
		strings.HasPrefix(email_line, " ") ||
		strings.HasPrefix(email_line, "\t")
}

// Re-add lost mixed-part declaration
func repair_gnats_boundary(raw_email string) string {
	var result string
	matching_header := true
	had_boundary, boundary := scan_email_boundary(raw_email)

	// No boundary declaration to add
	if !had_boundary {
		return raw_email
	}

	scanner := bufio.NewScanner(strings.NewReader(raw_email))
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		// Reached the first non-header
		if matching_header && !is_email_header(scanner.Text()) {
			// Stop looking for more headers
			matching_header = false
			// Add our boundary
			result += "Content-Type: multipart/mixed; boundary=\"" + boundary + "\"\r\n"
		}
		result += scanner.Text()
		result += "\n"
	}

	return result

}

func scan_email_boundary(raw_email string) (had_boundary bool, first_boundary string) {
	var new_boundary string
	had_boundary = false

	scanner := bufio.NewScanner(strings.NewReader(raw_email))
	scanner.Split(bufio.ScanLines)

	for scanner.Scan() {
		// Does this look like a boundary?
		if strings.HasPrefix(scanner.Text(), "--") {
			new_boundary = strings.TrimPrefix(scanner.Text(), "--")
			if !had_boundary {
				first_boundary = new_boundary
				had_boundary = true
			}
		}
	}

	// The last boundary should be a terminating one.
	// (As opposed to just something starting with --)
	if new_boundary != first_boundary+"--" {
		had_boundary = false
	}

	return had_boundary, first_boundary
}

type parsed_pair struct {
	key   string
	value string
}

func pair_value_by_key(dictionary []parsed_pair, match_key string) string {
	for _, pair := range dictionary {
		if pair.key == match_key {
			return pair.value
		}
	}
	panic("No match to " + match_key)
}

func valid_gnats_key_start(text string) bool {
	if !strings.HasPrefix(text, ">") {
		return false
	}
	current_match0 := strings.SplitN(text, ":", 2)
	if len(current_match0) != 2 {
		return false
	}
	key := current_match0[0]
	switch key {
	case ">Number":
	case ">Category":
	case ">Synopsis":
	case ">Confidential":
	case ">Severity":
	case ">Priority":
	case ">Responsible":
	case ">State":
	case ">Class":
	case ">Sub,itted-Id":
	case ">Arrival-Date":
	case ">Closed-Date":
	case ">Last-Modified":
	case ">Originator":
	case ">Release":
	case ">Organization":
	case ">Environment":
	case ">Description":
	case ">How-To-Repeat":
	case ">Fix":
	case ">Release-Note":
	case ">Audit-Trail":
	case ">Unformatted":
	default:
		return false
	}
	return true
}

func split_to_attributes(text string) []parsed_pair {
	scanner := bufio.NewScanner(strings.NewReader(text))
	scanner.Split(bufio.ScanLines)

	var dictionary []parsed_pair
	var keep_state bool = false
	var current_match0 []string
	for scanner.Scan() {
		// About to reach next key:value pair
		// do we have a previous match to save?
		if valid_gnats_key_start(scanner.Text()) && keep_state {
			current_match1 := parsed_pair{
				key:   strings.TrimPrefix(current_match0[0], ">"),
				value: strings.TrimSpace(current_match0[1]),
			}
			dictionary = append(dictionary, current_match1)
			keep_state = false
		}

		if valid_gnats_key_start(scanner.Text()) {
			keep_state = true
			current_match0 = strings.SplitN(scanner.Text(), ":", 2)
		} else if keep_state {
			// multi-line match: keep old results
			current_match0[1] += "\n"
			current_match0[1] += scanner.Text()
		}
	}

	// End of text, let's knock out the last value we've been working on.
	if keep_state {
		current_match1 := parsed_pair{
			key:   strings.TrimPrefix(current_match0[0], ">"),
			value: strings.TrimSpace(current_match0[1]),
		}
		dictionary = append(dictionary, current_match1)
		keep_state = false
	}
	return dictionary
}

func checkErr(err error) {
	if err != nil {
		panic(err)
	}
}

func usage() {
	fmt.Printf("Usage: [GITEA_TOKEN=token] \t%s --gitea-server http://localhost:3000 --gitea-owner gnat-conversion-bot --gitea-repo gitea-conversion\n", os.Args[0])
        flag.PrintDefaults()
	os.Exit(1)
}
