maintain a file called VENDORS.md that list all the external dependencies for the backend and frontend in production.

maintain README.md with all the features and technical details of the application.

Try to limit external dependencies and use custom code when possible 
if custom code is not the best option try to use library supported by large corporation like google, aws, microsoft, etc.
Least favor option use community or single author library.

try to develop the system so that it can scale to 100,000 devices with 1,000 users.  Users must be connected via Websocket and receive changes in near real-time so that multiples users can work on the same area of a map.