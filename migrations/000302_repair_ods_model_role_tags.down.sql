BEGIN;

UPDATE platform.semantic_tags
SET sharing_scope='DOMAIN',updated_at=now()
WHERE sharing_scope='PLATFORM'
  AND governance='CONTROLLED'
  AND code::text LIKE 'system.%';

ALTER TABLE platform.semantic_tags
  DROP CONSTRAINT semantic_tags_no_cross_domain_sharing;
ALTER TABLE platform.semantic_tags
  ADD CONSTRAINT semantic_tags_no_cross_domain_sharing CHECK(
    sharing_scope<>'PLATFORM'
  );

COMMIT;
