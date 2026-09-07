# Vision

**Status:** Draft product direction.

**Last updated:** 8 September 2026.

The polling application is intended to help an invited group select winning
options using proportional representation by the Single Transferable Vote
(PR-STV), following Ireland's national voting system. A moderator defines the
poll, specifies the number of winning options, invites its voters, and sets a
closing deadline. Voters rank their preferences, revise them as often as needed
before that deadline, and participate without managing another password.

This vision develops the intent in the [product brief](../README.md).
It describes the desired experience and product boundaries. The
[architecture](ARCHITECTURE.md) defines the Go application's structure, technical
components, and infrastructure integration. Both documents are drafts; detailed
specifications and implementation follow. Irish PR-STV is the selected counting
system.

## Purpose

A group decision can involve more than a single acceptable option. Ranking
allows a voter to express their relative preferences across the options they
support. Participation should require an understanding of the question and the
options, without requiring an understanding of the counting machinery.

The proposed product brings poll administration, invitations, preference voting,
and the closing deadline into one coherent experience. Its value lies in making
the process understandable to voters and manageable for moderators.

## People and their needs

- **Voters** need to understand the question, rank the options they wish to
  support, and know whether their ballot has been accepted. They need a clear way
  to return and change their preferences while polling remains open.
- **Moderators** need to establish the question, options, electorate, and
  deadline, then provide voters with access to the poll. They need to recognize
  one participant even when that person appears under several email addresses.
  They also need to reuse an earlier poll's participant list and adjust it for
  the next poll. Moderator eligibility is established through configuration.
- **Operators** need a clear boundary between the polling application and its
  surrounding environment, including the Exodan configuration arrangement
  referenced in the original brief.

These are responsibilities. They do not establish a requirement for separate
accounts, administrative interfaces, or deployment components.

## Established product intent

The product has the following foundation:

- **Irish PR-STV counting.** Counts follow the rules of Ireland's national
  voting system. The application performs the count automatically; automation
  changes how the count is carried out, while preserving those counting rules.
- **Moderator-defined number of winners.** When creating a poll, the moderator
  specifies how many options can win, equivalent to the number of seats being
  filled in a constituency.
- **One vote per voter.** A ranked ballot expresses that vote. Changing the
  ballot before the deadline does not create an additional vote.
- **One participant, multiple email addresses.** A moderator may supply several
  email addresses for a participant, separated by commas or colons. Every listed
  address receives an invitation. A magic link used from any of those addresses
  leads to the same participant record and ballot for that poll.
- **Optional further preferences.** A voter numbers their choices from `1`
  onwards and may stop after the options they wish to rank. Ranking every option
  is optional, including in a poll with nine options.
- **Moderator-defined participation.** The moderator creates the poll, adds its
  participants with their associated email addresses, and sets the closing
  deadline.
- **Optional immediate announcement.** The moderator has an **announce the
  winner immediately** checkbox. When selected, the system announces the winning
  outcome as soon as counting finishes. Otherwise, the moderator announces the
  outcome outside the system.
- **Moderator access to results.** After the deadline, the moderator can log in
  and inspect the results and winning options, whether or not automatic
  announcement was selected.
- **Pause and early closing.** The moderator can pause polling if there is a
  problem. While paused, new votes and revisions stop. The moderator can then
  choose **Close poll and count results** to close the poll and trigger the
  automatic count manually.
- **Reusable participant lists.** The moderator may start with a previous
  poll's list, including each participant's grouped email addresses, and edit
  the new list before sending invitations.
- **A unique poll address.** The moderator receives a URL for the poll, and
  voters receive access by email.
- **Revisable ballots.** Voters may change their preferences any number of times
  while polling remains open, before the deadline.
- **Passwordless access.** Authentication uses a magic link delivered by email.
- **Short invitations and plain English help.** Invitation emails stay brief.
  The voting page provides readily available voting instructions and a separate
  explanation of counting and its benefits.
- **Configured moderators.** Moderator configuration follows Exodan's encrypted
  YAML arrangement, `secrets/<host>.yaml.age`, with the decrypted configuration
  supplied to the application. This corrects the original brief's `.age.yaml`
  spelling to the verified contract convention.

## Why Irish PR-STV

The choice of Irish PR-STV is deliberate:

- **Familiarity:** the project owner knows the Irish system well.
- **Defined rules:** Ireland's national system provides an established basis for
  counting the votes.
- **Simple voting:** voters number their preferences and may stop after the
  options they wish to rank. The counting process can handle complex edge cases
  without making voting or the explanation of how to vote complicated.

The Electoral Commission's [explanation of Ireland's voting system](https://www.electoralcommission.ie/irelands-voting-system/)
provides the public overview of preference voting, quotas, and transfers. The
application uses the moderator's chosen number of winning options in the role
played by seats in a constituency.

The detailed national counting reference is Part XIX, Rules for the Counting of
the Votes, of the [Electoral Act 1992](https://www.irishstatutebook.ie/eli/1992/act/23/enacted/en/html).
That link is the enacted text. The counting specification must establish the
applicable rules and amendments, including quotas, surplus transfers,
exclusions, ballots with no remaining usable preference, ties, and other edge
cases. The system is selected; the detailed translation into software remains
specification work. Automation changes the execution of the count, while
retaining those rules.

These references establish the counting authority for development. The
voter-facing help below keeps its explanation focused on the poll.

## Desired experience

### Invite a person through the addresses they use

A readers' group mailing list may contain the same person twice under different
email addresses. The moderator may not know which mailbox that participant will
use when voting. Those addresses can therefore be entered together for one
participant, and each receives an invitation.

The participant can follow a magic link received at either address and reach the
same voting record. Switching addresses provides access to the same current
ballot and the same opportunity to revise it before closing. It does not provide
another vote. The moderator's grouping of addresses establishes that they belong
to one participant.

### Set up a clear decision

The moderator should be able to see what voters will be deciding, which options
are available, how many can win, who is invited, and when polling ends. Voters
should encounter the same question and options throughout the poll. Whether any
of these details can change after invitations have been sent remains a product
decision.

### Prepare the next poll from an existing list

A moderator running another poll for the readers' group should be able to reuse
the previous poll's participant list. The moderator can review the list, add or
remove participants, and correct their email addresses before sending the new
invitations. Reuse preserves the grouping of multiple addresses for one person.

The prepared list belongs to the new poll. Editing it leaves the previous poll's
electorate and ballots unchanged. Reusing participants carries forward the
invitation information. A participant's vote in the previous poll does not carry
forward.

### Express preferences with confidence

The voting experience should explain what the numbers mean and make it clear
that further preferences are optional. Ranking should be understandable and
accessible, with clear feedback about the ballot being submitted. The intended
experience should accommodate keyboard use and assistive technology; a specific
interface or accessibility standard has not yet been selected.

Help should be easy to find on the voting page. A separate **How will votes be
counted?** link should expand an explanation or open a help page. Both explanations
use plain English, roughly at the level understandable by a twelve-year-old,
without talking down to the reader. They stay focused on this poll, without
mentioning Ireland, national voting systems, or elections. The further-reading
link goes to Wikipedia's Single Transferable Vote article.

### Allow a change of mind

The ability to revise a vote is central to the brief. Returning voters should
be able to understand their current submitted preferences, the deadline for
changing them, and whether polling is open, paused, or closed. A successful
revision should leave them confident about which ballot will count.

### Make the outcome understandable

The proposed direction is to explain the outcome in terms of Irish PR-STV
counting rules and expressed preferences. Any presentation of results should
make clear what was counted and how the outcome was reached.

The moderator can log in after the deadline to inspect the results and winning
options. The **announce the winner immediately** checkbox determines whether the
system announces the outcome as soon as it is counted. If the box was not
checked, announcing the outcome is the moderator's responsibility outside the
system. The setting controls announcement; counting and moderator access to
results still take place.

### Handle a problem during polling

The moderator can pause polling to stop further votes and revisions while a
problem is considered. Pausing does not itself count the votes or announce an
outcome. The moderator can then choose **Close poll and count results**, ending
polling and triggering the application's count without waiting for the scheduled
deadline. The same announcement preference applies to that result.

## Voter-facing copy

The invitation uses the short copy below. `[thing]` is replaced with the subject
of the poll and `[link]` with the recipient's voting link. The help drafts provide
the intended level of detail for the voting page and its counting explanation.

### Invitation email

```markdown
Please vote for [thing] by clicking the link below:

[link]

You will be asked to vote by ranking your preferences 1, 2, 3 and so on.
You have what is called a *Single Transferable Vote*.
Every vote counts towards choosing the result.
Voter preferences count.
```

### How to vote

Number the options in the order you prefer them:

- Put **1** beside your first choice.
- Put **2** beside your second choice, **3** beside your third, and so on.
- Rank as many or as few options as you wish. Leave the others unranked.

These are your preferences for one vote. Your later choices tell the count where
your vote can go next.

Submit your rankings when ready. While polling is open, you can return and
change them as often as you wish before the deadline shown on the poll. Your
latest submitted rankings are the ones that count. If polling is paused or
closed, further changes are unavailable.

### How will votes be counted?

The application counts the votes automatically after voting closes. The poll
shows how many winning options it will choose.

1. **Count first choices.** Each vote starts with its first preference.
2. **Set a winning target.** The number of valid votes and the number of winning
   places determine how many votes an option needs to secure a place.
3. **Choose winners and transfer extra support.** An option that reaches the
   target wins a place. Votes above the target can transfer to other options,
   following the next available preferences on the ballots being transferred.
4. **Transfer votes from options that drop out.** If no extra support can be
   transferred, an option with the fewest votes can be removed. Its votes move
   to the next available preferences.
5. **Continue until the places are filled.** The process repeats. If the number
   of options left equals the number of places left, those options win, even
   if they have not reached the target.

For example, if your first choice drops out and your second choice is still
available, your vote moves to your second choice. If none of your ranked options
is available, your vote cannot move further. The count never adds a preference
you did not express.

**Why use this system?**

- You can express a first choice and the alternatives you would prefer.
- Later preferences can still help choose the result when an earlier choice
  drops out or has more support than it needs.
- Where there are several winning places, different preferences within the
  group can be represented.

Further reading: [Single Transferable Vote on Wikipedia](https://en.wikipedia.org/wiki/Single_transferable_vote).

## Product boundaries

The current brief centres on polls with an electorate supplied by a moderator.
It does not establish public self-enrolment, social features, campaigning tools,
or a general survey builder as product goals.

Email-based access does not settle ballot secrecy or the visibility of individual
votes. The project needs an explicit privacy policy for voters, moderators, and
operators before it can make promises about anonymity or confidentiality.

## Signs of success

The following are proposed outcomes for later evaluation, rather than measured
results or signed-off acceptance criteria:

- Voters can rank as many options as they wish without uncertainty about what
  their preferences mean.
- Returning voters can revise their ballot and identify the version that will
  count.
- Participants invited through multiple email addresses reach the same ballot
  whichever address they use, and contribute only one vote to the poll.
- Moderators can establish a poll and invite its electorate without separately
  managing voters' passwords.
- Moderators can prepare the next poll from an earlier participant list, make
  the necessary edits, and then send invitations.
- Moderators can inspect the counted outcome, control automatic announcement,
  and pause polling before manually closing and counting when necessary.
- Participants understand the closing deadline and the explanation of the
  outcome made available to them.
- Voters can find a plain English explanation of voting and counting without
  needing to understand the system's technical or national background.
- The service's access and privacy promises are clear enough for participants
  to make an informed decision about using it.

## Delivery direction

The Go application server drives all interactions and the user experience.
JavaScript should be avoided wherever possible and used only where absolutely
unavoidable. The architecture applies this principle through server-rendered
pages and ordinary HTML forms.

The delivery priority is to reuse suitable code from the upload and writeback
projects to reach an initial working application quickly. The
[reuse plan](ARCHITECTURE.md#reuse-from-upload-and-writeback) identifies concrete
starting points in their application wiring, configuration, magic links,
templates, email, and automation. Reuse should support the polling experience and
its agreed rules. The architecture records the necessary adaptations; code has
not yet been incorporated.

Exodan owns deployment and host operation. The Go application owns poll
behaviour, data, authentication, and counting. This division follows Exodan's
project integration contract, as recorded in the architecture.

## Decisions needed to develop the vision

- **Announcement delivery:** the delivery channel and content for automatic
  announcements, and the default state of the announcement checkbox.
- **Visibility:** access to turnout information, individual ballots, and detailed
  counting information, including what is visible before closing.
- **Administration:** whether moderators can change options, invitations, or
  deadlines after a poll has begun; how a paused poll is resumed; and what happens
  if the scheduled deadline arrives while polling is paused.
- **Participant administration:** handling an address entered against more than
  one participant, and correcting a participant's addresses after invitations or
  voting have begun.
- **Privacy:** what information is retained and who is permitted to inspect
  participant addresses and ballots.
- **Moderator authority:** whether moderators may administer only their own
  polls, and whether an invited moderator may also vote.
- **Ballot validation:** how invalid, repeated, or incomplete rankings are
  handled and explained, while preserving optional further preferences.
- **Timing:** the precise acceptance boundary for submissions at the deadline
  and how deadlines are presented across timezones.
- **Returning access:** magic-link lifetime and reuse, session lifetime, and
  how a voter returns after a link or session expires.

These questions refine the product contract. The Irish PR-STV counting system,
moderator-defined number of winners, and Go-driven interactions are established
directions. Detailed product rules will be developed in specification sheets;
the application structure is defined in the architecture.
