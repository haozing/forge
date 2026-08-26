BEGIN;

CREATE OR REPLACE FUNCTION retrieval.matches_field_filters(fields jsonb, predicates jsonb)
RETURNS boolean
LANGUAGE plpgsql IMMUTABLE STRICT AS $$
DECLARE
  predicate jsonb;
  field_name text;
  field_value jsonb;
  expected jsonb;
  op text;
  expected_exists boolean;
BEGIN
  IF jsonb_typeof(fields) <> 'object' OR jsonb_typeof(predicates) <> 'array' THEN
    RETURN false;
  END IF;

  FOR predicate IN SELECT value FROM jsonb_array_elements(predicates)
  LOOP
    field_name := predicate ->> 'field';
    expected := predicate -> 'value';
    op := predicate ->> 'operator';

    IF field_name IS NULL OR field_name = '' OR op IS NULL THEN
      RETURN false;
    END IF;

    IF op = 'exists' THEN
      IF jsonb_typeof(expected) <> 'boolean' THEN
        RETURN false;
      END IF;
      expected_exists := (expected #>> '{}')::boolean;
      IF (fields ? field_name) <> expected_exists THEN
        RETURN false;
      END IF;
      CONTINUE;
    END IF;

    IF NOT (fields ? field_name) THEN
      RETURN false;
    END IF;
    field_value := fields -> field_name;

    CASE op
      WHEN 'eq' THEN
        IF field_value <> expected THEN RETURN false; END IF;
      WHEN 'neq' THEN
        IF field_value = expected THEN RETURN false; END IF;
      WHEN 'in' THEN
        IF jsonb_typeof(expected) <> 'array'
           OR NOT EXISTS (SELECT 1 FROM jsonb_array_elements(expected) item WHERE item = field_value) THEN
          RETURN false;
        END IF;
      WHEN 'contains' THEN
        IF jsonb_typeof(field_value) = 'string' AND jsonb_typeof(expected) = 'string' THEN
          IF position(lower(expected #>> '{}') IN lower(field_value #>> '{}')) = 0 THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'array' THEN
          IF jsonb_typeof(expected) = 'array' THEN
            IF NOT (field_value @> expected) THEN RETURN false; END IF;
          ELSIF NOT (field_value @> jsonb_build_array(expected)) THEN
            RETURN false;
          END IF;
        ELSIF jsonb_typeof(field_value) = 'object' AND jsonb_typeof(expected) = 'object' THEN
          IF NOT (field_value @> expected) THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      WHEN 'contains_any' THEN
        IF jsonb_typeof(expected) <> 'array' THEN
          RETURN false;
        ELSIF jsonb_typeof(field_value) = 'array' THEN
          IF NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements(field_value) actual
            JOIN jsonb_array_elements(expected) wanted ON wanted = actual
          ) THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'string' THEN
          IF NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements(expected) wanted
            WHERE jsonb_typeof(wanted) = 'string'
              AND position(lower(wanted #>> '{}') IN lower(field_value #>> '{}')) > 0
          ) THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      WHEN 'gte' THEN
        IF jsonb_typeof(field_value) = 'number' AND jsonb_typeof(expected) = 'number' THEN
          IF (field_value #>> '{}')::numeric < (expected #>> '{}')::numeric THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'string' AND jsonb_typeof(expected) = 'string' THEN
          IF (field_value #>> '{}') < (expected #>> '{}') THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      WHEN 'lte' THEN
        IF jsonb_typeof(field_value) = 'number' AND jsonb_typeof(expected) = 'number' THEN
          IF (field_value #>> '{}')::numeric > (expected #>> '{}')::numeric THEN RETURN false; END IF;
        ELSIF jsonb_typeof(field_value) = 'string' AND jsonb_typeof(expected) = 'string' THEN
          IF (field_value #>> '{}') > (expected #>> '{}') THEN RETURN false; END IF;
        ELSE
          RETURN false;
        END IF;
      ELSE
        RETURN false;
    END CASE;
  END LOOP;
  RETURN true;
END
$$;

COMMIT;
