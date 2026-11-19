UPDATE vacancy
SET   company  = '',
      identity = '|' || substr(identity, 9)
WHERE company = 'Unknown';
