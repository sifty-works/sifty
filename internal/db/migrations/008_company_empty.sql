-- Store empty string instead of 'Unknown' for vacancies whose company is not
-- known. listing.Identity("", title) = "|" + normalised_title; for rows that
-- had company='Unknown' the stored identity is 'unknown|<normalised_title>',
-- so the new identity is '|' || substr(identity, 9) (skipping 'unknown|').
UPDATE vacancy
SET   company  = '',
      identity = '|' || substr(identity, 9)
WHERE company = 'Unknown';
