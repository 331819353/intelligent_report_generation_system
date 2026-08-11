ALTER TABLE askdata.conversations
  ADD COLUMN custom_label text CHECK(
    custom_label IS NULL OR (
      char_length(custom_label) BETWEEN 1 AND 120
      AND custom_label=btrim(custom_label)
      AND custom_label !~ '[[:cntrl:]]'
    )
  );

COMMENT ON COLUMN askdata.conversations.custom_label IS
  'Actor-managed display title for conversation history; never stores the raw question by default';
