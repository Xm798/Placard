// Package placardskill embeds the two Placard Claude Code skill documents:
// SKILL.md is the skill definition itself, INSTALL.md the one-time install
// guide. Both files in this directory are the single source of truth; the
// server re-serves the embedded copies at GET /skill.md and GET /install.md
// so agents can self-install the skill.
// (go:embed cannot reference files outside the package directory, hence this
// small package instead of embedding from internal/handler.)
package placardskill

import (
	"bytes"
	_ "embed"
	"strconv"
	"strings"
)

//go:embed SKILL.md
var SkillMD []byte

//go:embed INSTALL.md
var InstallMD []byte

// SkillVersion is the `version:` field of SKILL.md's frontmatter — a build-time
// invariant echoed as `skill_version` in publish/restore responses so installed
// agents can detect a stale local copy and re-fetch /skill.md.
var SkillVersion int

func init() {
	// Fail fast: a SKILL.md whose frontmatter lacks a parseable version is a
	// build defect, not a runtime condition.
	const malformed = "placardskill: SKILL.md frontmatter must declare a positive integer version"
	if !bytes.HasPrefix(SkillMD, []byte("---\n")) {
		panic(malformed)
	}
	body := SkillMD[len("---\n"):]
	end := bytes.Index(body, []byte("\n---"))
	if end < 0 {
		panic(malformed)
	}
	for _, line := range strings.Split(string(body[:end]), "\n") {
		if !strings.HasPrefix(line, "version:") {
			continue
		}
		v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "version:")))
		if err != nil || v < 1 {
			panic(malformed)
		}
		SkillVersion = v
		return
	}
	panic(malformed)
}
