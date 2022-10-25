FROM node:11.1

WORKDIR /jetmon

# RUN apk add --no-cache python3 make g++

RUN npm config set unsafe-perm true
RUN npm install -g node-gyp@8.4.1

# Get the dependencies loaded first - this makes rebuilds faster
COPY package.json .
RUN npm install

COPY . .

CMD [ "bash", "run-jetmon.sh" ]
