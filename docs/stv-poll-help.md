# STV Poll

Usage:

    stv-poll serve
    stv-poll create-poll
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
