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
* The server window becomes a control-plane, with instructions for users to SSH
  in, and a view of connected users
* Users may SSH in with any unique username and are prompted to input a SSN
* If the username is already claimed, the user is prompted to choose a new one
* The control-plane displays the username and SSN requesting to join the
  session
* The owner can accept or decline the request
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
