DROP TRIGGER IF EXISTS semantic_tool_calls_immutable
  ON platform.semantic_tool_calls;
DROP FUNCTION IF EXISTS platform.reject_semantic_tool_call_mutation();
DROP TABLE IF EXISTS platform.semantic_tool_calls;
