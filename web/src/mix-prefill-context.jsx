import React,{createContext,useContext,useState} from 'react';
import {mixProposalFresh} from './native-mix-proposal-client.js';
const Context=createContext(null);
export function MixPrefillProvider({children}){const[draft,setDraft]=useState(null);return <Context.Provider value={{draft,offer:p=>{if(!mixProposalFresh(p))throw Error('Proposal expired. Request a fresh saved-selection proposal.');setDraft({proposal:structuredClone(p),key:crypto.randomUUID()});},clear:()=>setDraft(null)}}>{children}</Context.Provider>;}
export const useMixPrefill=()=>useContext(Context);
