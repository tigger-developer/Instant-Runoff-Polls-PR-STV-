# STV Poll

STV Poll will help an invited group choose one or more winning options by ranking
preferences. Each participant has a **Single Transferable Vote (STV)**. The
application will count votes automatically using PR-STV guided by the Irish
counting system. This is an online poll; conformity to election legislation is
not a product requirement.

The product vision, application architecture, and three build specifications are
recorded. Native web checks use Biome for CSS and tidy-html5 for rendered HTML;
Go linting remains separate. The work ledger links each deliverable and its
current evidence.

## Documentation

- [Build work ledger](docs/work.org): the three specifications for the Go
  foundation, Irish PR-STV counting, and the invited poll workflow, with links to
  their definitions and dependencies.
- [Vision](docs/VISION.md): purpose, desired experience, voter-facing copy,
  product boundaries, and the history of approved product decisions.
- [Architecture](docs/ARCHITECTURE.md): the Go application's components,
  dependencies, runtime flows, persistence, reuse plan, and Exodan integration.
- This README: the product brief and requirements captured so far. These are
  inputs to the linked specifications, which contain the acceptance criteria.

## Local foundation

The foundation is a Go service. It needs Go, `golangci-lint`, `govulncheck`,
Biome and tidy-html5 installed through the local development environment.

Build and run the retained checks from the repository root:

```sh
make build
```

```sh
make lint
```

```sh
make test
```

For local serving, create an empty state directory and supply the Exodan-shaped
runtime values. Run the binary from `cmd/stv-poll`, where the packaged template
and static-asset paths exist:

```sh
mkdir -p .local/state
```

Copy `secrets/localhost.yaml.example` to the ignored
`secrets/localhost.yaml`, then replace the signing-key placeholder with the
base64 encoding of 32 random bytes. The example moderator address is synthetic.

```sh
cd cmd/stv-poll
DEFAULT_CONFIG_PATH=../../config/defaults.yaml \
CONFIG_PATH=../../config/localhost.yaml.example \
SECRETS_PATH=../../secrets/localhost.yaml \
STATE_DIRECTORY=../../.local/state \
ADDR=127.0.0.1:8080 \
../../bin/stv-poll serve
```

Run the same configuration through the bounded background-work entry point:

```sh
cd cmd/stv-poll
DEFAULT_CONFIG_PATH=../../config/defaults.yaml \
CONFIG_PATH=../../config/localhost.yaml.example \
SECRETS_PATH=../../secrets/localhost.yaml \
STATE_DIRECTORY=../../.local/state \
../../bin/stv-poll process-due-work
```

`CONFIG_PATH` and `SECRETS_PATH` are optional overlays. In deployment, Exodan
supplies all runtime paths and `ADDR`; the application does not configure
domains, routing, TLS, or host services. `make install PREFIX=/path` copies the
binary, help, templates, static assets and non-secret defaults beneath that
prefix. Run the installed binary from `PREFIX/share/stv-poll`.

For a consistent backup, stop both `serve` and every `process-due-work`
invocation, copy the SQLite files under `STATE_DIRECTORY`, and then restart the
processes. Restore only while both process types are stopped; replace the state
files with the saved copy before starting either process.

## How a poll works

1. A configured moderator creates a poll, supplies its question and options,
   chooses the number of winners, and sets the deadline and announcement
   preference. The poll has a unique URL.
2. The moderator prepares the participant list, either from scratch or by
   reusing a previous poll's list, and edits it before sending invitations.
3. Each listed email address receives a short invitation with a magic link.
   Following that link provides passwordless access to the participant's ballot.
4. The voter ranks their preferences and submits them. They can return and
   revise their vote any number of times while polling is open, before the
   deadline.
5. Polling normally closes at the deadline. If a problem arises, the moderator
   can pause polling and then choose **Close poll and count results**.
6. The application counts the final eligible ballots. The moderator can inspect
   the results and winning options. The announcement setting determines whether
   the application announces the outcome or the moderator announces it outside
   the system.

## Captured requirements

### Moderators and poll administration

Moderators are pre-configured through the Exodan configuration arrangement;
moderator self-registration is outside the current brief. A moderator defines
the options, number of winning options, electorate, closing deadline, and
announcement preference, and receives the poll's unique URL.

The **number of winners** is set when creating the poll. It is the equivalent of
the number of seats being filled in a constituency and is an input to the count.
A poll may select one winner or several winners.

### One participant, several email addresses

A participant is one invited person with one voting entitlement in a poll. The
administrator supplies the participant's display name. Email addresses are ways
to invite and authenticate that participant. In the moderator form, enter one
participant per line as **name, colon, then comma-separated addresses**:

```text
Alex Reader: reader.personal@example.org, reader.group@example.org
```

Every listed address receives an invitation. A magic link from any of those
addresses reaches the **same participant record and current ballot**. Voting
through one address and returning through another cannot create a second vote.

The moderator explicitly groups the addresses. The application does not infer
that separately entered participants are the same person. The approved workflow
rejects the same normalized address across two participants in one poll, and
de-duplicates repeated addresses within a group. Opening freezes the grouping;
later corrections use a new draft.

### Reuse a participant list

A moderator can start a new poll with the participant list from a previous poll.
Each participant's display name and grouped email addresses are preserved. The
moderator can add or remove participants and edit their names or addresses before
sending the new invitations.

The new electorate belongs to the new poll. Editing it leaves the earlier poll's
electorate and ballots unchanged. Previous votes are never carried forward; each
participant has one voting entitlement in each poll in which they are included.

### Voting and returning to a ballot

Voters number options in preference order: **1** for their first choice, **2** for
their second, and so on. Ranking every option is optional. For example, a poll
with nine options allows a voter to rank all nine or just the options they wish
to support.

The ballot is the participant's current ordered preferences for the poll. A
revision replaces the effective preferences for that single vote. Revisions are
unlimited while polling is open, before the deadline, whichever associated email
address the participant uses to return.

Authentication uses email magic links without passwords. Knowing the poll's URL
does not establish voting entitlement or moderator authority. Sending an
invitation and accepting a ballot are separate events: the voter needs clear
confirmation that their submitted preferences were accepted.

### Pause, close, and count

The moderator can pause polling when there is a problem. Paused and closed polls
accept neither new ballots nor revisions. Pausing alone does not close the poll,
count votes, or announce a result.

From a paused poll, **Close poll and count results** closes voting and initiates
the automatic count without waiting for the deadline. Closing at the deadline
and closing manually both establish the final eligible ballots. Authentication
does not extend the voting period.

The count uses **PR-STV guided by the Irish counting system**, with the poll's
options as candidates and the configured number of winners as seats. The
application performs the count automatically. Its counting specification defines
the exact rules and worked test cases, including transfers, rounding, ties, and
termination. Legislation provides guidance; statutory compliance and tracking
legislative amendments are not acceptance requirements. The rationale and
reference material are captured in the [vision](docs/VISION.md#why-irish-pr-stv).

### Results and announcement

The moderator has an **announce the winner immediately** checkbox:

- **Checked:** the system announces the winning outcome as soon as counting
  finishes, including after a manual close and count.
- **Unchecked:** the system does not announce the outcome. The moderator
  inspects the results and announces the winner outside the system.

The moderator can log in after the deadline to inspect the results and winning
options. Results are also available after a manual close and count. Counting and
moderator access do not depend on the announcement setting. The approved
workflow defaults the checkbox off and uses a short individual email to each
invited address when checked. It creates no public result page.

### Plain English guidance

Invitation emails use the [short invitation copy](docs/VISION.md#invitation-email).
The voting page provides readily available **How to vote** help and a separate
**How will votes be counted?** explanation, either expanded on the page or
reached through a link.

Help explains the count and its benefits in plain English, understandable by a
twelve-year-old. It stays focused on the poll, without mentioning Ireland,
national voting systems, or elections. Optional further reading links to
Wikipedia's Single Transferable Vote article. The full draft copy is preserved
in the [vision](docs/VISION.md#voter-facing-copy).

## Application direction

The application will be written in **Go**, with the application server driving
all interactions and user experience. Avoid JavaScript wherever possible; use it
only where absolutely unavoidable.

The architecture uses one Go binary, server-rendered HTML, and SQLite. Suitable
code from `../upload` and `../writeback` will provide a starting point for
application wiring, configuration, magic links, templates, email, and durable
background work. The [architecture's reuse plan](docs/ARCHITECTURE.md#reuse-from-upload-and-writeback)
identifies the inspected source files and adaptations.

The application consumes Exodan's infrastructure contract. Exodan owns deployment
and host operation; STV Poll owns application behaviour and data. Moderator
configuration and other secrets follow that contract's encrypted YAML convention,
`secrets/<host>.yaml.age`. The application reads the decrypted file supplied by
Exodan. The exact runtime boundary is documented in the
[architecture](docs/ARCHITECTURE.md#exodan-integration).

## Remaining product decisions

The [vision's decision list](docs/VISION.md#decisions-needed-to-develop-the-vision)
preserves the original questions and their resolutions for privacy, result visibility,
announcements, moderator authority, participant corrections, ballot validation,
and poll timing. The [poll workflow specification](specs/003-invited-poll-workflow/spec.org)
records the explicit policies approved with all three specifications on
8 September 2026. The earlier open-question and proposal wording is preserved
as definition history, not as unresolved product choices. The
[counting specification](specs/002-irish-pr-stv-counting/spec.org)
defines the detailed counting procedure and worked cases.

## Documentation history

The original brief was titled *Instant Runoff Polls* and used `PR-SRV`; this
expanded brief uses PR-STV and explicitly covers one or more winners. The original
`.age.yaml` reference is corrected to Exodan's verified `.yaml.age` convention.

The first architecture draft's roles, domain meanings, participant rules, poll
journey, voting guidance, and unresolved product questions have been consolidated
into this README and the vision. The architecture now defines the application
structure. These are relocations and clarifications of captured intent; the
earlier wording remains in Git history.

On 8 September 2026, the operator clarified that this is an online poll. The
earlier requirement for legislative conformity is withdrawn. Irish PR-STV remains
the guide; the application's approved counting specification governs its behaviour.
