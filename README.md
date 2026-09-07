# STV Poll

STV Poll will help an invited group choose one or more winning options by ranking
preferences. Each participant has a **Single Transferable Vote (STV)**. The
application will count votes automatically using Ireland's national PR-STV rules.

**Project stage:** product vision and application architecture. This repository
currently contains documentation; the Go application has not been implemented.
Specification sheets are the next stage.

## Documentation

- [Vision](docs/VISION.md): purpose, desired experience, voter-facing copy,
  product boundaries, and unresolved product decisions.
- [Architecture](docs/ARCHITECTURE.md): the Go application's components,
  dependencies, runtime flows, persistence, reuse plan, and Exodan integration.
- This README: the product brief and requirements captured so far. These are
  inputs to the forthcoming specifications, rather than acceptance criteria.

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

A participant is one invited person with one voting entitlement in a poll. An
email address is a way to invite and authenticate that participant. The moderator
may enter several addresses for one participant, separated by **commas or
colons**, for example:

```text
reader.personal@example.org, reader.group@example.org
```

Every listed address receives an invitation. A magic link from any of those
addresses reaches the **same participant record and current ballot**. Voting
through one address and returning through another cannot create a second vote.

The moderator explicitly groups the addresses. The application does not infer
that separately entered participants are the same person. Handling an address
assigned to two participant entries, and correcting groupings after voting has
begun, still need product decisions.

### Reuse a participant list

A moderator can start a new poll with the participant list from a previous poll.
The grouping of each participant's email addresses is preserved. The moderator
can add or remove participants and edit their addresses before sending the new
invitations.

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

The count follows **Ireland's national PR-STV rules**, using the poll's options
as candidates and the configured number of winners as seats. The application
performs the count automatically; this replaces counting by hand while retaining
the same rules. The rationale and counting authorities are captured in the
[vision](docs/VISION.md#why-irish-pr-stv).

### Results and announcement

The moderator has an **announce the winner immediately** checkbox:

- **Checked:** the system announces the winning outcome as soon as counting
  finishes, including after a manual close and count.
- **Unchecked:** the system does not announce the outcome. The moderator
  inspects the results and announces the winner outside the system.

The moderator can log in after the deadline to inspect the results and winning
options. Results are also available after a manual close and count. Counting and
moderator access do not depend on the announcement setting. The announcement
channel, message, and default checkbox state remain open decisions.

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
preserves the outstanding questions about privacy, result visibility,
announcements, moderator authority, participant corrections, ballot validation,
and poll timing. Those decisions and the detailed counting procedure belong in
the forthcoming specification work.

## Documentation history

The original brief was titled *Instant Runoff Polls* and used `PR-SRV`; this
expanded brief uses PR-STV and explicitly covers one or more winners. The original
`.age.yaml` reference is corrected to Exodan's verified `.yaml.age` convention.

The first architecture draft's roles, domain meanings, participant rules, poll
journey, voting guidance, and unresolved product questions have been consolidated
into this README and the vision. The architecture now defines the application
structure. These are relocations and clarifications of captured intent; the
earlier wording remains in Git history.
