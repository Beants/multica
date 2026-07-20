-- workflow_rule_binding: binds a workflow_rule to a target (node/template/
-- agent/project). enforcement=context_inject means the rule content is
-- injected into the target's handoff at dispatch (P1-4); gate_check means a
-- gate-type=rules node machine-checks it (P1-4b). No FK: rule_id / target_id
-- are logical refs enforced in app layer (rule may archive before binding is
-- cleaned up; target may be any of four types so no single FK could apply).

CREATE TABLE workflow_rule_binding (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    -- logical ref: workflow_rule(id), enforced in app layer
    rule_id UUID NOT NULL,
    target_type TEXT NOT NULL CHECK (target_type IN ('node', 'template', 'agent', 'project')),
    -- logical ref: target row id (node_key for node? resolved at query time)
    target_id UUID NOT NULL,
    enforcement TEXT NOT NULL DEFAULT 'context_inject' CHECK (enforcement IN ('gate_check', 'context_inject')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
