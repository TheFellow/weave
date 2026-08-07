CREATE TABLE greetings (
  id INTEGER PRIMARY KEY,
  message TEXT NOT NULL
);

CREATE VIEW recent_greetings AS
SELECT id, message FROM greetings;
