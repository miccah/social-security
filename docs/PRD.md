# Requirements
This document describes the functional requirements of this project.

## Functionality

* This project is operated by the owner, the user who starts the server
* The owner starts the server in any directory (the project)
* The server creates a sandboxed environment seeded with the contents of the
  directory, respecting .gitignore when copying files
* The server opens up an ngrok tunnel for SSH connections
* The owner is prompted to SSH in (via LAN), which starts the session
* The owner and all users are immediately placed into a shared tmux session
* The launching terminal becomes a daemon: it prints the SSH connect instructions
  (for guests and for the owner) and a running status log
* The owner's session is a single surface combining the shared editor with a
  private control overlay that connected users cannot see or reach
* An ambient status line shows connected/waiting counts without interrupting work
* Users may SSH in with any unique username and are prompted to input a SSN
* If the username is already claimed, the user is prompted to choose a new one
* A join request is surfaced to the owner ambiently, without stealing focus
* The owner opens the control overlay to view the requesting username and SSN and
  to accept, decline, or kick users
* At any point if the owner detaches or closes their tmux session, all users
  must disconnect, as the end of the session
* The server closes the ngrok tunnel
* The sandboxed environment is saved for 7 days
* On session end, the working directory is written back to the host
* If the project is a git repo, changes can also be pushed to the upstream remote


### Owner

As the owner,
* I want to be able to pair program on a project
* I want to know who is connected at all times
* I want to see who is requesting to join and accept or decline the request
* I want to be able to kick a user
* I want to manage the session (accept/decline/kick) without leaving my editor
* I don't want users to see or be able to trigger my controls
* I want the session to end when I disconnect
* I want the user to share their SSN out-of-band to confirm I am accepting the
  correct user


### User

As a user,
* I want to be able to pair program on a project
* I don't want to install any special programs on my client
* I want to be able to use my own username
* I want to input any SSN I want


## Non-functional requirements

* A transient disconnect on the owner's connection ends the session for
  everyone
* A kicked user is allowed to attempt to reconnect
* When the project is a git repo, the owner's git credentials are part of the VM
  to allow committing and pushing (by any user)


## Definitions

* A project is any directory; it may optionally be a git repo, which enables
  push-based reintegration
* A sandbox is an environment where users can modify files freely without
  harming the system (in a NixOS VM)
    * There are no limitations on commands
    * There are no limitations on the network
    * The security is "social" - the owner is watching what the users are doing
      and has an established relationship with the users
* A session is a shared tmux session where all clients can read/write for pair
  programming
* The control overlay is a surface rendered only to the owner, for managing join
  requests and users; it is invisible to and unreachable by connected users
