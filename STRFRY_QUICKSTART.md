# Strfry quickstart

Feedbuilder is a helper for strfry.  First you will need to compile strfry and get familiar with it's commands.

# Debian pre-reqs:
```
sudo apt install -y git g++ make libssl-dev zlib1g-dev liblmdb-dev libflatbuffers-dev libsecp256k1-dev libzstd-dev
```

# Compile
this will output a strfry binary in the current directory

```
git clone https://github.com/hoytech/strfry
cd strfry
git submodule update --init
make setup-golpe
make -j4
```

# Increase maximum number of open files and watches 

Strfry requires increasing the max open files and watches.  You can set them systemwide and have it be persistent with this script (run with sudo or root):

```
#!/bin/bash
if grep "max_user_watches" /etc/sysctl.conf; then
    echo "max_user_watches already set"
else
    echo fs.inotify.max_user_watches=524288 | tee -a /etc/sysctl.conf && sysctl -p
fi

if grep "max_user_instances" /etc/sysctl.conf; then
    echo "max_user_instances already set"
else
    echo fs.inotify.max_user_instances=8192 | tee -a /etc/sysctl.conf && sysctl -p
fi
```

# Strfry config
Some settings in the strfry config file may be necessary for you to access your relay.

Open up the strfry.conf file and edit the following

Set it to be accessible on your network (default is localhost only)
```
# Interface to listen on. Use 0.0.0.0 to listen on all interfaces (restart required)
#    bind = "127.0.0.1"
     bind = "0.0.0.0"
```

# Strfry commands and overview

Strfry the binary, can be run multiple times (at the same time) in order to perform operations like routing, streaming or scanning while the relay is also running.  It's default configuration is set to use the current working directory for it's strfry.conf, AND for it's data directory.

## relay
The strfry commands for feedbuilder we are working with are:

runs strfry in relay mode (listening and serving)
```
./strfry relay
```
CTRL-C to quit

## router

### feedbuilder
This is where feedbuilder comes in. Follow the main [README](README.md). To install feedbuilder in your working directory and run it to generate your strfry-router.config

then..

run the router process
```
./strfry router --config strfry-router.config
```
CTRL-C to quit

## multi-process
You wll want to run these as system services eventually.  For now you can just run them an get familiar with strfry commands.

They must be run simultaneously if you want to read from the relay while it routes.

See also: [strfry docs](https://github.com/hoytech/strfry?tab=readme-ov-file#running-a-relay)

# Troubleshooting

If you see an error like: "no such file .mdb" that means strfry can't find it's database in the current directory.  You can set the file path in strfry.conf or you can mkdir ./strfry-db and try again.