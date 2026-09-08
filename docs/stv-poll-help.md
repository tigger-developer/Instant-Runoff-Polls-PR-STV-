# STV Poll

Usage:

    stv-poll serve
    stv-poll create-poll
    stv-poll close-poll POLL_ID
    stv-poll count-audit POLL_ID
    stv-poll process-due-work
    stv-poll -h
    stv-poll --help
    stv-poll --version

`serve` requires `DEFAULT_CONFIG_PATH`, `STATE_DIRECTORY`, and `ADDR`.
`CONFIG_PATH` and `SECRETS_PATH` are optional configuration layers when supplied.

## Create an invited poll

`create-poll` reads one YAML poll definition from standard input. Each named
participant may have one or more email recipients. Poll creation stores one
participant identity and queues one invitation message for that participant;
all listed addresses receive the same participant-scoped magic link.

The Exodan `APP-admin` wrapper supplies the application identity, working
directory, configuration, secrets, and state directory. From the application
repository, an operator can submit the committed example without copying it to
the host:

    ssh HOST 'sudo -u stv-poll stv-poll-admin create-poll' < fixtures/poll.example.yaml

The command writes a JSON summary containing the poll URL and number of queued
participant invitations. The YAML structure is demonstrated in
`fixtures/poll.example.yaml`.

## Process queued work

`process-due-work` performs one bounded pass over pending close, count, and
delivery work, then writes a JSON summary and exits.

The Exodan deployment must enable the `local_mail` capability. Exodan supplies
`SENDMAIL_PATH` and `MAIL_DEFAULT_SENDER_DOMAIN` to the worker. STV Poll submits
one complete message through that adapter using `stv-poll@<provided-domain>` as
both its envelope sender and `From` header; it receives no SMTP credential.

## Close and count a poll

`close-poll POLL_ID` immediately closes an open or paused poll, freezes its
anonymized ballots, and queues the count. It does not wait for the configured
deadline. Run the bounded worker to perform the queued count:

    ssh HOST 'sudo -u stv-poll stv-poll-admin close-poll POLL_ID'
    ssh HOST 'sudo -u stv-poll stv-poll-admin process-due-work'

After the count succeeds, `count-audit POLL_ID` writes one JSON document with
the option mapping, frozen anonymized input, input fingerprint, any recorded
lot decisions, and the result:

    ssh HOST 'sudo -u stv-poll stv-poll-admin count-audit POLL_ID'
