import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { ActionsMenu } from "@/components/ConfirmAction";
import { HealthBadge } from "@/components/badges";
import { TooltipProvider } from "@/components/ui/primitives";
import { ToastProvider } from "@/components/ui/toast";
import { worker } from "./fixtures";

function wrap(ui: React.ReactNode) {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <TooltipProvider>
        <ToastProvider>{ui}</ToastProvider>
      </TooltipProvider>
    </QueryClientProvider>,
  );
}

describe("ActionsMenu", () => {
  it("offers the allowed actions only and confirms a stop with its meaning", async () => {
    const user = userEvent.setup();
    wrap(<ActionsMenu worker={worker({ allowedActions: ["drain", "stop", "kill"] })} />);
    await user.click(screen.getByRole("button", { name: /Actions of/ }));
    const items = screen.getAllByRole("menuitem").map((e) => e.textContent);
    expect(items).toEqual(["Drain", "Stop", "Kill"]);
    await user.click(screen.getByRole("menuitem", { name: "Stop" }));
    expect(await screen.findByText(/reported to Automata as SELFTERMINATED/)).toBeInTheDocument();
  });

  it("is disabled without actions", () => {
    wrap(<ActionsMenu worker={worker({ allowedActions: [] })} />);
    expect(screen.getByRole("button", { name: /Actions of/ })).toBeDisabled();
  });
});

describe("HealthBadge", () => {
  it("shows the health, and nothing for NONE", () => {
    const { container, rerender } = wrap(<HealthBadge health="STUCK" reasons={["NO_PROGRESS"]} />);
    expect(screen.getByText("Stuck")).toBeInTheDocument();
    rerender(
      <QueryClientProvider client={new QueryClient()}>
        <TooltipProvider>
          <HealthBadge health="NONE" />
        </TooltipProvider>
      </QueryClientProvider>,
    );
    expect(container.textContent).toBe("");
  });
});
