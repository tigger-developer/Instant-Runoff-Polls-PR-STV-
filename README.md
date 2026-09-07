# Instant Runoff Polls

This system implements Instant Runoff polling according to the rules of PR-SRV.

- Each voter gets one vote
- It is called the **Single Transferable Vote (STV)**
- Voting is easy: you simply number the options in order of preference, with 1 being your first choice, 2 your second choice, and so on. You do not have to rank all options, you can just mark the ones you like, or e.g. in a 9-option poll you can number 1 to 9.
- Moderator sets up the poll and gets a unique url, and sets the email addresses of the voters, and a deadline for polling to end.
- Voters receive an email with the unique url and can cast their votes until the deadline. Voters can change their votes any number of times before the deadline.
- Passwordless, authentication is via magic link to email.
- Moderators are pre-configured in a config file in a .age.yaml file per exodan contract
