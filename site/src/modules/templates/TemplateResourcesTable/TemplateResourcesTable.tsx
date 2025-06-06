import type { WorkspaceResource } from "api/typesGenerated";
import { AgentRowPreview } from "modules/resources/AgentRowPreview";
import { Resources } from "modules/resources/Resources";
import type { FC } from "react";

interface TemplateResourcesProps {
	resources: WorkspaceResource[];
}

export const TemplateResourcesTable: FC<TemplateResourcesProps> = ({
	resources,
}) => {
	return (
		<Resources
			resources={resources}
			agentRow={(agent, count) => (
				// Align values if there are more than one row
				// When it is only one row, it is better to have it "flex" and not hard aligned
				<AgentRowPreview key={agent.id} agent={agent} alignValues={count > 1} />
			)}
		/>
	);
};
