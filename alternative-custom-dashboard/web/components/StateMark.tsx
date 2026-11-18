import { State, stateLabel } from "@/lib/api";

export function StateMark({ state, text = true }: { state: State; text?: boolean }) {
  return (
    <span className={`statemark s-${state}`}>
      <span className="dot" aria-hidden />
      {text ? stateLabel[state] : <span className="sr">{stateLabel[state]}</span>}
    </span>
  );
}
